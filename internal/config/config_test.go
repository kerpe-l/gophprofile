package config

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/logger"
)

// mapEnv подменяет окружение процесса картой, чтобы тесты не правили
// глобальное состояние и могли идти параллельно.
func mapEnv(m map[string]string) getenv {
	return func(key string) string { return m[key] }
}

// requiredEnv — минимальный набор переменных, без которых старт невозможен.
func requiredEnv() map[string]string {
	return map[string]string{
		envDatabaseDSN: "postgres://user:pass@localhost:5432/gophprofile?sslmode=disable",
		envS3Endpoint:  "localhost:9000",
		envS3AccessKey: "access",
		envS3SecretKey: "secret",
		envAMQPURL:     "amqp://guest:guest@localhost:5672/",
	}
}

func TestLoadServerMissingSecrets(t *testing.T) {
	t.Parallel()

	_, err := loadServer(mapEnv(map[string]string{}))
	require.Error(t, err)

	for _, key := range []string{envDatabaseDSN, envS3Endpoint, envS3AccessKey, envS3SecretKey, envAMQPURL} {
		assert.ErrorContains(t, err, key+" is required")
	}
}

func TestLoadServerDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := loadServer(mapEnv(requiredEnv()))
	require.NoError(t, err)

	assert.Equal(t, EnvLocal, cfg.App.Env)
	assert.Equal(t, slog.LevelInfo, cfg.App.LogLevel)
	assert.Equal(t, logger.FormatText, cfg.App.LogFormat)
	assert.Equal(t, defaultHTTPAddr, cfg.HTTP.Addr)
	assert.Equal(t, defaultHTTPReadTimeout, cfg.HTTP.ReadTimeout)
	assert.Equal(t, defaultHTTPRequestTimeout, cfg.HTTP.RequestTimeout)
	assert.Equal(t, defaultDBMaxConns, cfg.DB.MaxConns)
	assert.Equal(t, defaultS3Bucket, cfg.S3.Bucket)
	assert.False(t, cfg.S3.UseSSL)
	assert.Equal(t, defaultAMQPPrefetch, cfg.AMQP.Prefetch)
	assert.Equal(t, defaultImageMaxUploadBytes, cfg.Image.MaxUploadBytes)
	assert.Equal(t, defaultImageMaxPixels, cfg.Image.MaxPixels)
	assert.Equal(t, defaultImageJPEGQuality, cfg.Image.JPEGQuality)
}

func TestLoadServerOverrides(t *testing.T) {
	t.Parallel()

	env := requiredEnv()
	env[envAppEnv] = EnvProduction
	env[envLogLevel] = "debug"
	env[envHTTPAddr] = "127.0.0.1:9090"
	env[envHTTPReadTimeout] = "42s"
	env[envHTTPRequestTimeout] = "7s"
	env[envS3UseSSL] = "true"
	env[envS3Bucket] = "pictures"
	env[envAMQPPrefetch] = "16"
	env[envImageMaxUploadBytes] = "1048576"
	env[envImageJPEGQuality] = "70"

	cfg, err := loadServer(mapEnv(env))
	require.NoError(t, err)

	assert.Equal(t, EnvProduction, cfg.App.Env)
	assert.Equal(t, slog.LevelDebug, cfg.App.LogLevel)
	// Формат явно не задан: в продакшне по умолчанию JSON.
	assert.Equal(t, logger.FormatJSON, cfg.App.LogFormat)
	assert.Equal(t, "127.0.0.1:9090", cfg.HTTP.Addr)
	assert.Equal(t, 42*time.Second, cfg.HTTP.ReadTimeout)
	assert.Equal(t, 7*time.Second, cfg.HTTP.RequestTimeout)
	assert.True(t, cfg.S3.UseSSL)
	assert.Equal(t, "pictures", cfg.S3.Bucket)
	assert.Equal(t, 16, cfg.AMQP.Prefetch)
	assert.Equal(t, int64(1<<20), cfg.Image.MaxUploadBytes)
	assert.Equal(t, 70, cfg.Image.JPEGQuality)
}

func TestLoadServerInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "app env", key: envAppEnv, value: "staging", wantErr: envAppEnv},
		{name: "log level", key: envLogLevel, value: "loud", wantErr: envLogLevel},
		{name: "log format", key: envLogFormat, value: "xml", wantErr: envLogFormat},
		{name: "duration", key: envHTTPReadTimeout, value: "soon", wantErr: envHTTPReadTimeout},
		{name: "non positive duration", key: envS3Timeout, value: "0s", wantErr: envS3Timeout + " must be positive"},
		{name: "integer", key: envDBMaxConns, value: "many", wantErr: envDBMaxConns},
		{name: "non positive integer", key: envAMQPPrefetch, value: "0", wantErr: envAMQPPrefetch + " must be positive"},
		{name: "int64", key: envImageMaxPixels, value: "50m", wantErr: envImageMaxPixels},
		{name: "bool", key: envS3UseSSL, value: "maybe", wantErr: envS3UseSSL},
		{name: "jpeg quality above range", key: envImageJPEGQuality, value: "101", wantErr: envImageJPEGQuality + " must be at most 100"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := requiredEnv()
			env[tc.key] = tc.value

			_, err := loadServer(mapEnv(env))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// Воркер не слушает HTTP, поэтому негодная секция HTTP валит только сервер.
func TestLoadWorkerIgnoresHTTPSection(t *testing.T) {
	t.Parallel()

	env := requiredEnv()
	env[envHTTPReadTimeout] = "0s"

	_, err := loadServer(mapEnv(env))
	require.ErrorContains(t, err, envHTTPReadTimeout)

	cfg, err := loadWorker(mapEnv(env))
	require.NoError(t, err)
	assert.Equal(t, defaultAMQPTimeout, cfg.AMQP.Timeout)
}

func TestLoadWorkerMissingSecrets(t *testing.T) {
	t.Parallel()

	_, err := loadWorker(mapEnv(map[string]string{}))
	require.ErrorContains(t, err, envDatabaseDSN+" is required")
	require.ErrorContains(t, err, envAMQPURL+" is required")
}

// Мигратору хватает одного DSN: ни хранилище, ни брокер ему не нужны.
func TestLoadMigratorNeedsOnlyDatabase(t *testing.T) {
	t.Parallel()

	cfg, err := loadMigrator(mapEnv(map[string]string{
		envDatabaseDSN: "postgres://user:pass@localhost:5432/gophprofile?sslmode=disable",
	}))
	require.NoError(t, err)
	assert.Equal(t, defaultDBQueryTimeout, cfg.DB.QueryTimeout)
}

func TestLoadMigratorMissingDSN(t *testing.T) {
	t.Parallel()

	env := requiredEnv()
	delete(env, envDatabaseDSN)

	_, err := loadMigrator(mapEnv(env))
	require.ErrorContains(t, err, envDatabaseDSN+" is required")
}
