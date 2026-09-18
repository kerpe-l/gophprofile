// Package reconciler по тикеру доделывает то, что сорвалось между базой,
// хранилищем и брокером: переводит зависшие uploading в failed, заново
// публикует события загрузки для uploaded + pending и события удаления для
// записей с неубранными файлами. Шаги прохода независимы.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

// tracerName — имя инструментации пакета в трейсах.
const tracerName = "github.com/kerpe-l/gophprofile/internal/worker/reconciler"

// Repository — метаданные аватаров.
type Repository interface {
	// SelectStuck перебирает не более limit загруженных, но не обработанных
	// аватаров, не менявшихся до момента before.
	SelectStuck(ctx context.Context, before time.Time, limit int) iter.Seq2[domain.Avatar, error]
	// SelectUncleaned перебирает не более limit удалённых аватаров
	// и сорвавшихся загрузок с неубранными файлами, не менявшихся до момента
	// before.
	SelectUncleaned(ctx context.Context, before time.Time, limit int) iter.Seq2[domain.Avatar, error]
	// FailStaleUploads переводит в failed загрузки, застрявшие в uploading
	// с момента before, и возвращает их число.
	FailStaleUploads(ctx context.Context, before time.Time) (int64, error)
}

// Publisher — публикация событий обработки.
type Publisher interface {
	// Publish отправляет событие и возвращается после подтверждения брокером.
	Publish(ctx context.Context, event broker.Event) error
}

// Config — параметры добора.
type Config struct {
	// Interval — период между проходами.
	Interval time.Duration
	// StuckAfter должен быть заметно больше времени штатной загрузки
	// и обработки, иначе добор вмешивается в то, что идёт прямо сейчас.
	StuckAfter time.Duration
	// Batch — сколько записей разбирается за проход; без предела выборка
	// вычитает таблицу целиком.
	Batch int
}

// Reconciler доделывает сорвавшиеся загрузки, обработки и уборки.
type Reconciler struct {
	repo      Repository
	publisher Publisher
	cfg       Config
	tracer    trace.Tracer
	log       *slog.Logger
}

// New собирает добор из его зависимостей.
func New(repo Repository, publisher Publisher, cfg Config, log *slog.Logger) *Reconciler {
	return &Reconciler{repo: repo, publisher: publisher, cfg: cfg, tracer: otel.Tracer(tracerName), log: log}
}

// Run выполняет проходы по тикеру, пока не отменят контекст.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.reconcile(ctx); err != nil {
				r.log.ErrorContext(ctx, "reconcile", slog.Any("error", err))
			}
		}
	}
}

// reconcile выполняет один проход. Перебор внутри шага прекращается на первой
// ошибке — остальное подберёт следующий тик.
//
// Спан прохода корневой: входящего контекста трейса у тикера нет. Через
// публикацию он связывает обработку переопубликованных событий с проходом.
func (r *Reconciler) reconcile(ctx context.Context) error {
	ctx, span := r.tracer.Start(ctx, "reconcile")
	defer span.End()

	before := time.Now().Add(-r.cfg.StuckAfter)

	failed, failErr := r.repo.FailStaleUploads(ctx, before)
	if failErr != nil {
		failErr = fmt.Errorf("fail stale uploads: %w", failErr)
	}

	republished, republishErr := r.republishUploads(ctx, before)
	cleanups, cleanupErr := r.republishCleanups(ctx, before)

	span.SetAttributes(
		attribute.Int64("failed_uploads", failed),
		attribute.Int("republished", republished),
		attribute.Int("cleanups", cleanups),
	)

	if failed > 0 || republished > 0 || cleanups > 0 {
		r.log.InfoContext(ctx, "reconciled",
			slog.Int64("failed_uploads", failed),
			slog.Int("republished", republished),
			slog.Int("cleanups", cleanups),
		)
	}

	if err := errors.Join(failErr, republishErr, cleanupErr); err != nil {
		return observability.SpanError(span, err)
	}

	return nil
}

// republishUploads публикует заново события загрузки, не дошедшие до брокера.
func (r *Reconciler) republishUploads(ctx context.Context, before time.Time) (int, error) {
	published := 0

	for avatar, err := range r.repo.SelectStuck(ctx, before, r.cfg.Batch) {
		if err != nil {
			return published, fmt.Errorf("select stuck uploads: %w", err)
		}

		// Идентификатор сообщения каждый раз новый: дедупликации по нему нет.
		event := broker.NewUploadEvent(avatar.ID, avatar.UserID, avatar.S3Key)
		if err := r.publisher.Publish(ctx, event); err != nil {
			return published, fmt.Errorf("republish upload event of avatar %s: %w", avatar.ID, err)
		}

		published++
	}

	return published, nil
}

// republishCleanups публикует события удаления для записей, чьи файлы
// не убраны: событие не дошло, уборка сорвалась или загрузка оборвалась.
func (r *Reconciler) republishCleanups(ctx context.Context, before time.Time) (int, error) {
	published := 0

	for avatar, err := range r.repo.SelectUncleaned(ctx, before, r.cfg.Batch) {
		if err != nil {
			return published, fmt.Errorf("select uncleaned avatars: %w", err)
		}

		event := broker.NewDeleteEvent(avatar.ID, avatar.StorageKeys())
		if err := r.publisher.Publish(ctx, event); err != nil {
			return published, fmt.Errorf("publish delete event of avatar %s: %w", avatar.ID, err)
		}

		published++
	}

	return published, nil
}
