// Package migrate применяет миграции схемы к PostgreSQL.
package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/kerpe-l/gophprofile/migrations"
)

// Up применяет все ещё не применённые миграции и возвращает их версии в
// порядке применения. Пустой результат означает, что схема уже актуальна.
//
// Соединение открывает и закрывает вызывающий: goose умеет закрыть переданный
// ему *sql.DB сам, но владение остаётся у того, кто соединение создал.
func Up(ctx context.Context, db *sql.DB) ([]int64, error) {
	// Provider вместо пакетных SetDialect/SetBaseFS: те пишут в глобальное
	// состояние, общее на весь процесс.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return nil, fmt.Errorf("init migration provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	versions := make([]int64, 0, len(results))
	for _, res := range results {
		versions = append(versions, res.Source.Version)
	}

	return versions, nil
}
