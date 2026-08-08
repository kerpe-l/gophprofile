// Package handler обрабатывает события брокера: создаёт миниатюры загруженных
// аватаров и убирает из хранилища файлы удалённых.
//
// Обработчик идемпотентен: перед работой он смотрит на состояние записи в базе
// и подтверждает без действий событие для уже обработанного или удалённого
// аватара — повторная доставка не порождает повторной записи в хранилище.
//
// Своих горутин обработчик не заводит и состояния не имеет: конкурентность
// ограничена пулом консьюмера, из всех горутин которого зовётся один и тот же
// экземпляр.
//
// Ошибки наружу делятся на две группы. Обёрнутые в broker.ErrNonRetryable
// неисправимы повтором — битый файл, неподдерживаемый формат, пропавший
// оригинал; такое событие подтверждается, а запись переводится в failed.
// Всё остальное — отказы базы и хранилища — уходит на лестницу повторов.
package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

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
	log         *slog.Logger
}

// New собирает обработчик из его зависимостей.
//
// maxOriginalBytes ограничивает размер оригинала, вычитываемого из хранилища;
// decodeConcurrency — число одновременных декодирований, минимум одно.
func New(
	repo Repository, storage Storage, processor Processor,
	maxOriginalBytes int64, decodeConcurrency int, log *slog.Logger,
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
		log:              log,
	}
}

// Handle обрабатывает одно доставленное событие.
//
// Тип события берётся из свойства сообщения, а не из ключа маршрутизации:
// при возврате с лестницы повторов ключ переписывается, и диспетчеризация
// по нему после первого же повтора уехала бы не туда.
func (h *Handler) Handle(ctx context.Context, msg broker.Message) error {
	switch msg.Type {
	case broker.RoutingKeyUploaded:
		return h.uploaded(ctx, msg)
	case broker.RoutingKeyDeleted:
		return h.deleted(ctx, msg)
	default:
		return nonRetryable(fmt.Errorf("unknown event type %q in message %s", msg.Type, msg.MessageID))
	}
}
