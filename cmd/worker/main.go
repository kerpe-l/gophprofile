// Команда worker обрабатывает события брокера: создаёт миниатюры,
// удаляет файлы из хранилища и добирает зависшие загрузки.
package main

import (
	"context"
	"fmt"
	"log/slog"
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

	// Консьюмер и reconciler подключаются здесь; пока воркер только
	// демонстрирует корректное завершение по сигналу.
	<-ctx.Done()
	log.Info("shutdown signal received")

	log.Info("worker stopped")

	return nil
}
