// Команда migrator применяет миграции схемы GophProfile и завершается.
// Контейнер с ней отрабатывает до старта server и worker.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	// Драйвер database/sql, поверх которого работает goose: сам goose
	// драйверы не регистрирует, это ответственность бинарника.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kerpe-l/gophprofile/internal/buildinfo"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/logger"
	"github.com/kerpe-l/gophprofile/internal/migrate"
)

// driverName — имя, под которым pgx регистрируется в database/sql.
const driverName = "pgx"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrator: %v\n", err)
		os.Exit(1)
	}
}

func run() (err error) {
	cfg, err := config.LoadMigrator()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.App.LogLevel, cfg.App.LogFormat)
	log.Info("starting migrator",
		slog.Any("build", buildinfo.Get()),
		slog.String("env", cfg.App.Env),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := sql.Open(driverName, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close database: %w", closeErr)
		}
	}()

	// Миграции идут одна за другой, параллельных запросов у мигратора нет.
	db.SetMaxOpenConns(1)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DB.QueryTimeout)
	defer cancel()

	if pingErr := db.PingContext(pingCtx); pingErr != nil {
		return fmt.Errorf("ping database: %w", pingErr)
	}

	versions, err := migrate.Up(ctx, db)
	if err != nil {
		return err
	}

	if len(versions) == 0 {
		log.Info("schema is already up to date")

		return nil
	}

	log.Info("migrations applied",
		slog.Int("count", len(versions)),
		slog.Any("versions", versions),
	)

	return nil
}
