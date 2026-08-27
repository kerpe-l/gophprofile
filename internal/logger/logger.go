// Package logger собирает slog-логгер приложения и связывает его с контекстом
// запроса: идентификатор запроса и координаты активного спана попадают в
// каждую запись, сделанную с этим контекстом.
package logger

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Format — формат вывода логов.
type Format string

// Поддерживаемые форматы вывода: JSON для продакшна, текст для локальной
// разработки.
const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// Имена полей записи лога, заполняемых из контекста.
const (
	requestIDAttr = "request_id"
	traceIDAttr   = "trace_id"
	spanIDAttr    = "span_id"
)

// New собирает логгер с указанным уровнем и форматом вывода. Записи, сделанные
// с контекстом (методы *Context), получают поле request_id, если оно положено
// в контекст через WithRequestID. Неизвестный формат трактуется как FormatJSON.
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

// Handle добавляет к записи request_id и координаты активного спана из
// контекста. trace_id пишется и для несэмплированного спана: корреляция
// логов между сервисами нужна независимо от того, экспортирован ли трейс.
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String(requestIDAttr, id))
	}

	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String(traceIDAttr, sc.TraceID().String()),
			slog.String(spanIDAttr, sc.SpanID().String()),
		)
	}

	return h.Handler.Handle(ctx, r)
}

// WithAttrs сохраняет обёртку: иначе логгер, полученный через slog.With,
// перестал бы добавлять поля из контекста.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup сохраняет обёртку по той же причине, что и WithAttrs.
// request_id при этом уезжает внутрь группы, как любой другой атрибут.
func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name)}
}
