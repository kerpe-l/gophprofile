// Package logger собирает slog-логгер приложения и связывает его с контекстом
// запроса: идентификатор запроса кладётся в контекст один раз и дальше сам
// попадает в каждую запись, сделанную с этим контекстом.
package logger

import (
	"context"
	"io"
	"log/slog"
)

// Format — формат вывода логов.
type Format string

// Поддерживаемые форматы вывода.
const (
	// FormatJSON — машинночитаемый вывод для продакшна.
	FormatJSON Format = "json"
	// FormatText — читаемый глазами вывод для локальной разработки.
	FormatText Format = "text"
)

// requestIDAttr — имя поля с идентификатором запроса в записи лога.
const requestIDAttr = "request_id"

// New собирает логгер с указанным уровнем и форматом вывода.
// Все записи, сделанные с контекстом (методы *Context), получают поле
// request_id, если оно положено в контекст через WithRequestID.
// Неизвестный формат трактуется как FormatJSON: логгер нужен раньше,
// чем появляется возможность сообщить об ошибке конфигурации.
func New(w io.Writer, level slog.Level, format Format) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler

	switch format {
	case FormatText:
		h = slog.NewTextHandler(w, opts)
	case FormatJSON:
		h = slog.NewJSONHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}

	return slog.New(contextHandler{Handler: h})
}

// requestIDKey — ключ контекста для идентификатора запроса.
// Тип неэкспортируемый, поэтому значение не может быть перезаписано снаружи.
type requestIDKey struct{}

// WithRequestID возвращает контекст с идентификатором запроса.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID возвращает идентификатор запроса из контекста.
// Если его там нет, возвращается пустая строка.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// contextHandler подмешивает в запись поля, извлечённые из контекста.
type contextHandler struct {
	slog.Handler
}

// Handle добавляет к записи request_id из контекста, если он там есть.
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String(requestIDAttr, id))
	}

	return h.Handler.Handle(ctx, r)
}

// WithAttrs сохраняет обёртку: иначе логгер, полученный через slog.With,
// перестал бы добавлять поля из контекста.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup сохраняет обёртку по той же причине, что и WithAttrs.
// У логгера с группой request_id попадает внутрь этой группы: обёртка
// добавляет поле уже после открытия группы. Терять поле совсем — хуже.
func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name)}
}
