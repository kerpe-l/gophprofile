// Package reconciler добирает загрузки, событие о которых не дошло до брокера.
//
// Публикация не входит в транзакцию с записью в базу и загрузкой в хранилище:
// атомарности между тремя системами нет. Процесс, умерший между переводом
// записи в uploaded и публикацией события, оставляет аватар с лежащим
// в хранилище оригиналом и необработанным навсегда. Такие записи — uploaded
// и pending, не менявшиеся дольше отсечки — перебираются по тикеру, и событие
// для них публикуется заново.
package reconciler

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Repository — метаданные аватаров.
type Repository interface {
	// SelectStuck перебирает аватары, оригинал которых загружен, а обработка
	// не начиналась, и которые не менялись до момента before, — не более
	// limit штук.
	SelectStuck(ctx context.Context, before time.Time, limit int) iter.Seq2[domain.Avatar, error]
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
	// StuckAfter — возраст, начиная с которого загрузка без начатой обработки
	// считается зависшей. Должен быть заметно больше времени штатной обработки,
	// иначе добор дублирует события, которые прямо сейчас разбирает воркер.
	StuckAfter time.Duration
	// Batch — сколько записей разбирается за один проход. Ограничение
	// обязательно: выборка без предела способна вычитать таблицу целиком.
	Batch int
}

// Reconciler переопубликовывает события для зависших загрузок.
type Reconciler struct {
	repo      Repository
	publisher Publisher
	cfg       Config
	log       *slog.Logger
}

// New собирает добор из его зависимостей.
func New(repo Repository, publisher Publisher, cfg Config, log *slog.Logger) *Reconciler {
	return &Reconciler{repo: repo, publisher: publisher, cfg: cfg, log: log}
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
				r.log.ErrorContext(ctx, "reconcile stuck uploads", slog.Any("error", err))
			}
		}
	}
}

// reconcile публикует события для зависших записей одного прохода.
//
// Перебор прекращается на первой же ошибке: и отказ базы, и отказ публикации
// означают, что остальные записи этого прохода тоже не пройдут, — их подберёт
// следующий тик.
func (r *Reconciler) reconcile(ctx context.Context) error {
	before := time.Now().Add(-r.cfg.StuckAfter)

	republished := 0

	for avatar, err := range r.repo.SelectStuck(ctx, before, r.cfg.Batch) {
		if err != nil {
			return fmt.Errorf("select stuck uploads: %w", err)
		}

		// Идентификатор сообщения каждый раз новый: идемпотентность держится
		// на состоянии записи в базе, а не на дедупликации по идентификатору.
		event := broker.NewUploadEvent(avatar.ID, avatar.UserID, avatar.S3Key)
		if err := r.publisher.Publish(ctx, event); err != nil {
			return fmt.Errorf("republish upload event of avatar %s: %w", avatar.ID, err)
		}

		republished++
	}

	if republished > 0 {
		r.log.InfoContext(ctx, "stuck uploads republished", slog.Int("count", republished))
	}

	return nil
}
