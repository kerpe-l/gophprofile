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

func TestRequestIDFromContext(t *testing.T) {
	t.Parallel()

	ctx := logger.WithRequestID(t.Context(), "req-42")
	assert.Equal(t, "req-42", logger.RequestID(ctx))
	assert.Empty(t, logger.RequestID(t.Context()))
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

// У логгера с группой request_id остаётся полем верхнего уровня, а всё,
// что относится к группе, лежит внутри неё: иначе поиск по логам зависел бы
// от того, где именно сделана запись.
func TestRequestIDStaysTopLevelWithGroup(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).
		With(slog.String("component", "server")).
		WithGroup("http").
		With(slog.String("method", "GET"))

	log.InfoContext(logger.WithRequestID(t.Context(), "req-4"), "request handled",
		slog.Int("status", 200))

	rec := decode(t, &buf)
	assert.Equal(t, "req-4", rec["request_id"])
	assert.Equal(t, "server", rec["component"], "атрибуты до группы остаются снаружи")

	group, ok := rec["http"].(map[string]any)
	require.True(t, ok, "group http is missing")
	assert.Equal(t, "GET", group["method"])
	assert.InEpsilon(t, float64(200), group["status"], 0)
}

// Вложенные группы складываются одна в другую, а не схлопываются.
func TestNestedGroups(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).WithGroup("http").WithGroup("request")

	log.InfoContext(logger.WithRequestID(t.Context(), "req-5"), "request handled",
		slog.String("path", "/avatars"))

	rec := decode(t, &buf)
	assert.Equal(t, "req-5", rec["request_id"])

	outer, ok := rec["http"].(map[string]any)
	require.True(t, ok, "group http is missing")

	inner, ok := outer["request"].(map[string]any)
	require.True(t, ok, "group request is missing")
	assert.Equal(t, "/avatars", inner["path"])
}

// Логгер с группой, но без атрибутов: пустая группа в запись не попадает —
// так же, как у обработчика стандартной библиотеки.
func TestEmptyGroupIsOmitted(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).WithGroup("http")

	log.InfoContext(logger.WithRequestID(t.Context(), "req-6"), "request handled")

	rec := decode(t, &buf)
	assert.Equal(t, "req-6", rec["request_id"])
	assert.NotContains(t, rec, "http")
}

// Два логгера, ответвлённых от одного, не должны перетирать друг другу
// накопленные группой атрибуты.
func TestBranchedLoggersAreIndependent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	base := logger.New(&buf, slog.LevelInfo, logger.FormatJSON).WithGroup("http")

	first := base.With(slog.String("method", "GET"))
	second := base.With(slog.String("method", "POST"))

	first.InfoContext(t.Context(), "request handled")

	group, ok := decode(t, &buf)["http"].(map[string]any)
	require.True(t, ok, "group http is missing")
	assert.Equal(t, "GET", group["method"])

	buf.Reset()
	second.InfoContext(t.Context(), "request handled")

	group, ok = decode(t, &buf)["http"].(map[string]any)
	require.True(t, ok, "group http is missing")
	assert.Equal(t, "POST", group["method"])
}

func TestLevelFiltering(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := logger.New(&buf, slog.LevelWarn, logger.FormatJSON)

	log.InfoContext(t.Context(), "ignored")
	assert.Empty(t, buf.String())

	log.WarnContext(t.Context(), "kept")
	assert.Contains(t, buf.String(), "kept")
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
