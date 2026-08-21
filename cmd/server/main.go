// Команда server поднимает REST API GophProfile.
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
	"time"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/buildinfo"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
	"github.com/kerpe-l/gophprofile/internal/logger"
	"github.com/kerpe-l/gophprofile/internal/observability"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
	"github.com/kerpe-l/gophprofile/internal/repository/postgres"
	httpapi "github.com/kerpe-l/gophprofile/internal/server/http"
	"github.com/kerpe-l/gophprofile/internal/server/service"
	"github.com/kerpe-l/gophprofile/internal/server/web"
	"github.com/kerpe-l/gophprofile/internal/storage/s3"
)

// serviceName — имя сервиса в трейсах.
const serviceName = "gophprofile-server"

// otelShutdownTimeout — предел на финальный сброс очереди спанов.
const otelShutdownTimeout = 5 * time.Second

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

	tracing, err := observability.Setup(ctx, cfg.Otel, serviceName, cfg.App.Env, log)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}

	// Спаны сбрасываются после остановки сервера, но до закрытия зависимостей.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), otelShutdownTimeout)
		defer cancel()

		if err := tracing.Shutdown(flushCtx); err != nil {
			log.Error("shutdown tracing", slog.Any("error", err))
		}
	}()

	router, err := app.router(cfg, log)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
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
// Публикатор закрывается вместе с соединением брокера.
type deps struct {
	repo      *postgres.Repository
	storage   *s3.Storage
	broker    *broker.Conn
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
func (d *deps) router(cfg *config.Config, log *slog.Logger) (http.Handler, error) {
	svc := service.New(
		d.repo,
		d.storage,
		d.publisher,
		imageproc.New(cfg.Image),
		placeholder.New(),
		log,
	)

	pages, err := web.New(web.Deps{
		Service:        svc,
		MaxUploadBytes: cfg.Image.MaxUploadBytes,
		Log:            log,
	})
	if err != nil {
		return nil, fmt.Errorf("build web interface: %w", err)
	}

	return httpapi.New(httpapi.Deps{
		Service: svc,
		Checks: map[string]httpapi.Checker{
			httpapi.ComponentDB:     d.repo,
			httpapi.ComponentS3:     d.storage,
			httpapi.ComponentBroker: d.broker,
		},
		HTTP:           cfg.HTTP,
		MaxUploadBytes: cfg.Image.MaxUploadBytes,
		Web:            pages,
		Tracing:        cfg.Otel.Endpoint != "",
		Log:            log,
	}), nil
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
