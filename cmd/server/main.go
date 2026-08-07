// Команда server поднимает REST API и веб-интерфейс GophProfile.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/buildinfo"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
	"github.com/kerpe-l/gophprofile/internal/logger"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
	"github.com/kerpe-l/gophprofile/internal/repository/postgres"
	httpapi "github.com/kerpe-l/gophprofile/internal/server/http"
	"github.com/kerpe-l/gophprofile/internal/server/service"
	"github.com/kerpe-l/gophprofile/internal/storage/s3"
)

func main() {
	if err := run(); err != nil {
		// Ошибка конфигурации возникает раньше, чем появляется логгер,
		// поэтому единый выход из main пишет в stderr напрямую.
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadServer()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.App.LogLevel, cfg.App.LogFormat)
	log.Info("starting server",
		slog.Any("build", buildinfo.Get()),
		slog.String("env", cfg.App.Env),
		slog.String("addr", cfg.HTTP.Addr),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := setup(ctx, cfg, log)
	if err != nil {
		return err
	}

	defer app.close(log)

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           app.router(cfg, log),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	// Буфер на одно значение: горутина не повиснет на отправке, даже если
	// выход произошёл по сигналу и результат ListenAndServe никто не читает.
	srvErr := make(chan error, 1)

	go func() {
		srvErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-srvErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("listen %s: %w", cfg.HTTP.Addr, err)
		}

		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// Отдельный контекст: контекст сигнала уже отменён, а активные запросы
	// должны успеть дорабатать.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}

	log.Info("server stopped")

	return nil
}

// deps — зависимости сервера, живущие столько же, сколько процесс.
//
// Публикатор отдельного времени жизни не имеет: он открывает канал на
// соединении с брокером, и закрытие соединения закрывает канал вместе с ним.
type deps struct {
	repo      *postgres.Repository
	storage   *s3.Storage
	broker    *broker.Conn
	publisher *broker.Publisher
}

// setup подключается к зависимостям. Отказ на любом шаге закрывает уже
// открытое: иначе упавший старт оставляет за собой соединения.
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

	conn, err := broker.New(ctx, cfg.AMQP)
	if err != nil {
		d.close(log)

		return nil, fmt.Errorf("connect broker: %w", err)
	}

	d.broker = conn
	d.publisher = conn.Publisher()

	return d, nil
}

// router собирает сервис и маршруты поверх открытых зависимостей.
func (d *deps) router(cfg *config.Config, log *slog.Logger) http.Handler {
	svc := service.New(
		d.repo,
		d.storage,
		d.publisher,
		imageproc.New(cfg.Image),
		placeholder.New(),
		log,
	)

	return httpapi.New(httpapi.Deps{
		Service: svc,
		Checks: map[string]httpapi.Checker{
			httpapi.ComponentDB:     d.repo,
			httpapi.ComponentS3:     d.storage,
			httpapi.ComponentBroker: d.broker,
		},
		HTTP:           cfg.HTTP,
		MaxUploadBytes: cfg.Image.MaxUploadBytes,
		Log:            log,
	})
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
