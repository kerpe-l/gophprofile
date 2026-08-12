// Команда worker обрабатывает события брокера: создаёт миниатюры,
// удаляет файлы из хранилища и добирает зависшие загрузки.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/buildinfo"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
	"github.com/kerpe-l/gophprofile/internal/logger"
	"github.com/kerpe-l/gophprofile/internal/repository/postgres"
	"github.com/kerpe-l/gophprofile/internal/storage/s3"
	"github.com/kerpe-l/gophprofile/internal/worker/handler"
	"github.com/kerpe-l/gophprofile/internal/worker/reconciler"
)

func main() {
	if err := run(); err != nil {
		// Ошибка конфигурации возникает раньше, чем появляется логгер,
		// поэтому единый выход из main пишет в stderr напрямую.
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.App.LogLevel, cfg.App.LogFormat)
	log.Info("starting worker",
		slog.Any("build", buildinfo.Get()),
		slog.String("env", cfg.App.Env),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := setup(ctx, cfg, log)
	if err != nil {
		return err
	}

	defer app.close(log)

	// Добор живёт ровно столько же, сколько потребление: оборвавшееся
	// соединение с брокером оставило бы тикер публиковать события в никуда.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	wg.Go(func() {
		app.reconciler(cfg, log).Run(runCtx)
	})

	log.Info("worker started")

	// Отмена контекста прекращает приём новых сообщений; взятые в работу
	// дорабатываются, и только после этого Consume возвращается.
	err = app.consumer.Consume(ctx, app.handler(cfg, log).Handle)

	cancel()
	wg.Wait()

	if err != nil {
		return fmt.Errorf("consume events: %w", err)
	}

	log.Info("worker stopped")

	return nil
}

// deps — зависимости воркера, живущие столько же, сколько процесс.
//
// Консьюмер и публикатор своего времени жизни не имеют: они открывают каналы
// на соединении с брокером, и закрытие соединения закрывает каналы вместе с ним.
type deps struct {
	repo      *postgres.Repository
	storage   *s3.Storage
	broker    *broker.Conn
	consumer  *broker.Consumer
	publisher *broker.Publisher
}

// setup подключается к зависимостям, закрывая уже открытые при отказе.
func setup(ctx context.Context, cfg *config.Config, log *slog.Logger) (*deps, error) {
	d := &deps{}

	repo, err := postgres.New(ctx, cfg.DB)
	if err != nil {
		return nil, fmt.Errorf("connect repository: %w", err)
	}

	d.repo = repo

	storage, err := s3.New(ctx, cfg.S3)
	if err != nil {
		d.close(log)

		return nil, fmt.Errorf("connect storage: %w", err)
	}

	d.storage = storage

	conn, err := broker.New(ctx, cfg.AMQP, broker.WithProcessTimeout(cfg.Worker.ProcessTimeout))
	if err != nil {
		d.close(log)

		return nil, fmt.Errorf("connect broker: %w", err)
	}

	d.broker = conn
	d.publisher = conn.Publisher()

	consumer, err := conn.Consumer(log)
	if err != nil {
		d.close(log)

		return nil, fmt.Errorf("open consumer: %w", err)
	}

	d.consumer = consumer

	return d, nil
}

// handler собирает обработчик событий поверх открытых зависимостей.
func (d *deps) handler(cfg *config.Config, log *slog.Logger) *handler.Handler {
	return handler.New(d.repo, d.storage, imageproc.New(cfg.Image),
		cfg.Image.MaxUploadBytes, cfg.Worker.DecodeConcurrency, log)
}

// reconciler собирает добор зависших загрузок поверх открытых зависимостей.
func (d *deps) reconciler(cfg *config.Config, log *slog.Logger) *reconciler.Reconciler {
	return reconciler.New(d.repo, d.publisher, reconciler.Config{
		Interval:   cfg.Worker.ReconcileInterval,
		StuckAfter: cfg.Worker.StuckAfter,
		Batch:      cfg.Worker.ReconcileBatch,
	}, log)
}

// close закрывает зависимости в порядке, обратном открытию.
func (d *deps) close(log *slog.Logger) {
	if d.broker != nil {
		if err := d.broker.Close(); err != nil {
			log.Error("close broker connection", slog.Any("error", err))
		}
	}

	if d.storage != nil {
		d.storage.Close()
	}

	if d.repo != nil {
		d.repo.Close()
	}
}
