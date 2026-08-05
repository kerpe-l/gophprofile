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

	"github.com/kerpe-l/gophprofile/internal/buildinfo"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/logger"
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

	srv := &http.Server{
		Addr: cfg.HTTP.Addr,
		// Роутер появится вместе с HTTP-слоем; пока сервер отвечает 404 на любой путь.
		Handler:           http.NewServeMux(),
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
