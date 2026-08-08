package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/logger"
)

// decode разбирает единственную JSON-запись из буфера.
func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))

	return rec
}

func TestNewAddsRequestID(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON)

	log.InfoContext(logger.WithRequestID(t.Context(), "req-1"), "avatar uploaded")

	rec := decode(t, &buf)
	assert.Equal(t, "req-1", rec["request_id"])
	assert.Equal(t, "avatar uploaded", rec["msg"])
}

func TestNewWithoutRequestID(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON)

	log.InfoContext(t.Context(), "avatar uploaded")

	rec := decode(t, &buf)
	assert.NotContains(t, rec, "request_id")
}

// slog.With возвращает логгер с новым хендлером: обёртка обязана пережить
// и WithAttrs, и WithGroup, иначе поля из контекста молча пропадают.
func TestRequestIDSurvivesWith(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).With(slog.String("component", "server"))

	log.InfoContext(logger.WithRequestID(t.Context(), "req-2"), "request handled")

	rec := decode(t, &buf)
	assert.Equal(t, "req-2", rec["request_id"])
	assert.Equal(t, "server", rec["component"])
}

// Группу обёртка передаёт вложенному обработчику, поэтому request_id уезжает
// внутрь группы вместе с остальными атрибутами. Проверяется, что он не пропал:
// потеря контекстных полей — единственное, чем WithGroup здесь опасен.
func TestRequestIDSurvivesGroup(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).WithGroup("http")

	log.InfoContext(logger.WithRequestID(t.Context(), "req-4"), "request handled",
		slog.String("method", "GET"))

	group, ok := decode(t, &buf)["http"].(map[string]any)
	require.True(t, ok, "group http is missing")
	assert.Equal(t, "req-4", group["request_id"])
	assert.Equal(t, "GET", group["method"])
}

func TestFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format logger.Format
		isJSON bool
	}{
		{name: "json", format: logger.FormatJSON, isJSON: true},
		{name: "text", format: logger.FormatText, isJSON: false},
		{name: "unknown falls back to json", format: logger.Format("xml"), isJSON: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			log := logger.New(&buf, slog.LevelInfo, tc.format)
			log.InfoContext(logger.WithRequestID(t.Context(), "req-3"), "message")

			valid := json.Valid(bytes.TrimSpace(buf.Bytes()))
			assert.Equal(t, tc.isJSON, valid)
			assert.Contains(t, buf.String(), "req-3")
		})
	}
}
