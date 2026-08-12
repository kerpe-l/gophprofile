//go:build integration

package migrate_test

import (
	"database/sql"
	"testing"

	// Драйвер database/sql: тест ходит в базу тем же способом, что и мигратор.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kerpe-l/gophprofile/internal/migrate"
)

// postgresImage — та же версия, что в docker-compose.yml: миграции проверяются
// на том же сервере, на котором потом поедут.
const postgresImage = "postgres:16-alpine"

// startPostgres поднимает пустую базу и возвращает соединение с ней.
func startPostgres(t *testing.T) *sql.DB {
	t.Helper()

	ctx := t.Context()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("gophprofile"),
		tcpostgres.WithPassword("gophprofile"),
		testcontainers.WithWaitStrategy(
			// Postgres в этом образе стартует дважды: первый раз — чтобы
			// выполнить initdb-скрипты, и только второй готов принимать
			// соединения снаружи.
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	t.Cleanup(func() {
		assert.NoError(t, db.Close())
	})

	require.NoError(t, db.PingContext(ctx))

	return db
}

func TestUpAppliesSchema(t *testing.T) {
	t.Parallel()

	db := startPostgres(t)

	versions, err := migrate.Up(t.Context(), db)
	require.NoError(t, err)
	require.NotEmpty(t, versions, "no migrations applied to an empty database")

	t.Run("columns", func(t *testing.T) {
		t.Parallel()

		want := []string{
			"id", "user_id", "file_name", "mime_type", "size_bytes",
			"width", "height", "s3_key", "thumbnail_s3_keys",
			"upload_status", "processing_status", "retry_count",
			"created_at", "updated_at", "deleted_at",
		}

		for _, column := range want {
			var exists bool

			err := db.QueryRowContext(t.Context(), `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'avatars' AND column_name = $1
				)`, column).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "column %s is missing", column)
		}
	})

	t.Run("indexes", func(t *testing.T) {
		t.Parallel()

		for _, index := range []string{"idx_avatars_user_id", "idx_avatars_status"} {
			var exists bool

			err := db.QueryRowContext(t.Context(), `
				SELECT EXISTS (
					SELECT 1 FROM pg_indexes
					WHERE tablename = 'avatars' AND indexname = $1
				)`, index).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "index %s is missing", index)
		}
	})

	// Мигратор запускается при каждом подъёме окружения, поэтому повторный
	// прогон обязан быть пустым, а не падать и не накатывать заново.
	t.Run("second run is a no-op", func(t *testing.T) {
		t.Parallel()

		versions, err := migrate.Up(t.Context(), db)
		require.NoError(t, err)
		assert.Empty(t, versions)
	})
}

// Значения по умолчанию заданы схемой: запись создаётся с одним лишь набором
// обязательных полей, а статусы и счётчик попыток проставляет база.
func TestSchemaDefaults(t *testing.T) {
	t.Parallel()

	db := startPostgres(t)

	_, err := migrate.Up(t.Context(), db)
	require.NoError(t, err)

	var (
		id               string
		uploadStatus     string
		processingStatus string
		retryCount       int
	)

	err = db.QueryRowContext(t.Context(), `
		INSERT INTO avatars (user_id, file_name, mime_type, size_bytes, s3_key)
		VALUES ('u-1', 'photo.png', 'image/png', 1024, 'originals/u-1')
		RETURNING id, upload_status, processing_status, retry_count`,
	).Scan(&id, &uploadStatus, &processingStatus, &retryCount)
	require.NoError(t, err)

	assert.NotEmpty(t, id, "id is not filled by gen_random_uuid()")
	assert.Equal(t, "uploading", uploadStatus)
	assert.Equal(t, "pending", processingStatus)
	assert.Equal(t, 0, retryCount)
}
