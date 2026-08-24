// Package handler создаёт миниатюры и удаляет файлы по событиям брокера.
//
// Ошибки, обёрнутые в broker.ErrNonRetryable, повтору не подлежат: запись
// переводится в failed, событие подтверждается. Остальные, включая отказ
// самой этой записи, уходят на лестницу повторов.
package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

// tracerName — имя инструментации пакета в трейсах.
const tracerName = "github.com/kerpe-l/gophprofile/internal/worker/handler"

// attrAvatarID — имя атрибута спана с идентификатором аватара.
const attrAvatarID = "avatar_id"

// Repository — метаданные аватаров.
type Repository interface {
	// Get возвращает живой аватар по идентификатору либо domain.ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
	// SetProcessingStatus переводит запись в новое состояние обработки;
	// запрещённый переход — domain.ErrInvalidTransition.
	SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error
	// CompleteProcessing сохраняет ключи готовых миниатюр и завершает обработку.
	CompleteProcessing(ctx context.Context, id uuid.UUID, thumbnails map[domain.ThumbnailSize]string) error
	// IncrementRetry увеличивает счётчик попыток обработки.
	IncrementRetry(ctx context.Context, id uuid.UUID) error
}

// Storage — оригиналы и миниатюры в объектном хранилище.
type Storage interface {
	// Get открывает объект на чтение; отсутствие объекта — domain.ErrNotFound.
	Get(ctx context.Context, key string) (domain.StoredObject, error)
	// Put кладёт объект по ключу, перезаписывая существующий.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// DeleteMany удаляет объекты одним запросом; отсутствие объекта ошибкой
	// не считается.
	DeleteMany(ctx context.Context, keys []string) error
}

// Metrics — метрики обработки.
type Metrics interface {
	// ObserveProcessing учитывает одну попытку обработки загрузки.
	ObserveProcessing(success bool, took time.Duration)
}

// Processor — создание миниатюр.
type Processor interface {
	// Thumbnails создаёт миниатюры всех запрошенных размеров, готовые
	// к загрузке в хранилище.
	Thumbnails(r io.Reader, sizes []domain.ThumbnailSize) (map[domain.ThumbnailSize][]byte, error)
}

// Handler обрабатывает доставленные события.
type Handler struct {
	repo      Repository
	storage   Storage
	processor Processor
	// maxOriginalBytes — предел на объём, который обработчик вычитывает
	// из хранилища в память.
	maxOriginalBytes int64
	// decodeSlots ограничивает одновременные декодирования: prefetch
	// консьюмера пиковую память не ограничивает.
	decodeSlots chan struct{}
	metrics     Metrics
	tracer      trace.Tracer
	log         *slog.Logger
}

// New собирает обработчик из его зависимостей. maxOriginalBytes ограничивает
// вычитываемый оригинал, decodeConcurrency — число одновременных
// декодирований, минимум одно.
func New(
	repo Repository, storage Storage, processor Processor,
	maxOriginalBytes int64, decodeConcurrency int, metrics Metrics, log *slog.Logger,
) *Handler {
	if decodeConcurrency < 1 {
		decodeConcurrency = 1
	}

	return &Handler{
		repo:             repo,
		storage:          storage,
		processor:        processor,
		maxOriginalBytes: maxOriginalBytes,
		decodeSlots:      make(chan struct{}, decodeConcurrency),
		metrics:          metrics,
		tracer:           otel.Tracer(tracerName),
		log:              log,
	}
}

// Handle обрабатывает одно доставленное событие. Тип берётся из свойства
// сообщения: ключ маршрутизации при возврате с лестницы повторов переписан.
func (h *Handler) Handle(ctx context.Context, msg broker.Message) error {
	ctx, span := h.tracer.Start(ctx, "handle "+msg.Type, trace.WithAttributes(
		semconv.MessagingMessageID(msg.MessageID),
		attribute.Int("attempt", msg.Attempt),
	))
	defer span.End()

	if err := h.dispatch(ctx, msg); err != nil {
		return observability.SpanError(span, err)
	}

	return nil
}

// dispatch выбирает обработчик по типу события.
func (h *Handler) dispatch(ctx context.Context, msg broker.Message) error {
	switch msg.Type {
	case broker.RoutingKeyUploaded:
		return h.uploaded(ctx, msg)
	case broker.RoutingKeyDeleted:
		return h.deleted(ctx, msg)
	default:
		return nonRetryable(fmt.Errorf("unknown event type %q in message %s", msg.Type, msg.MessageID))
	}
}
