// Package logger собирает slog-логгер приложения и связывает его с контекстом
// запроса: идентификатор запроса кладётся в контекст один раз и дальше сам
// попадает в каждую запись, сделанную с этим контекстом.
package logger

import (
	"context"
	"io"
	"log/slog"
	"slices"
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
// request_id верхнего уровня, если оно положено в контекст через
// WithRequestID, — в том числе записи логгера с группой.
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
//
// Группы обработчик открывает сам, а не передаёт вложенному: request_id
// обязан лежать в корне записи при любом логгере, иначе искать его в
// хранилище логов пришлось бы то в корне, то внутри группы, имя которой
// зависит от места вызова.
type contextHandler struct {
	slog.Handler
	// deferred — вызовы WithGroup и WithAttrs, сделанные после открытия
	// первой группы, в порядке вызова. Handle сворачивает их в атрибуты
	// записи; до первой группы всё уходит вложенному обработчику напрямую.
	deferred []deferredCall
}

// deferredCall — отложенный WithGroup (задано group) или WithAttrs (attrs).
type deferredCall struct {
	group string
	attrs []slog.Attr
}

// Handle добавляет к записи request_id из контекста, если он там есть,
// и раскладывает по группам всё, что накопили WithGroup и WithAttrs.
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	id := RequestID(ctx)

	// Без групп запись не пересобирается: это путь, по которому идут
	// все записи логгера без WithGroup, то есть подавляющее большинство.
	if len(h.deferred) == 0 {
		if id != "" {
			r.AddAttrs(slog.String(requestIDAttr, id))
		}

		return h.Handler.Handle(ctx, r)
	}

	rec := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)

	if id != "" {
		rec.AddAttrs(slog.String(requestIDAttr, id))
	}

	rec.AddAttrs(h.groupAttrs(r)...)

	return h.Handler.Handle(ctx, rec)
}

// groupAttrs сворачивает отложенные вызовы вместе с атрибутами записи:
// свёртка идёт с конца, поэтому последняя открытая группа оказывается самой
// внутренней. Пустая группа отбрасывается — так же, как её отбросил бы
// обработчик стандартной библиотеки.
func (h contextHandler) groupAttrs(r slog.Record) []slog.Attr {
	attrs := make([]slog.Attr, 0, r.NumAttrs())

	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)

		return true
	})

	for i := len(h.deferred) - 1; i >= 0; i-- {
		call := h.deferred[i]

		if call.group == "" {
			attrs = append(slices.Clone(call.attrs), attrs...)

			continue
		}

		if len(attrs) == 0 {
			continue
		}

		attrs = []slog.Attr{{Key: call.group, Value: slog.GroupValue(attrs...)}}
	}

	return attrs
}

// WithAttrs сохраняет обёртку: иначе логгер, полученный через slog.With,
// перестал бы добавлять поля из контекста. Пока групп нет, атрибуты уходят
// вложенному обработчику — он их разбирает один раз, а не на каждой записи.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	if len(h.deferred) == 0 {
		return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
	}

	return contextHandler{Handler: h.Handler, deferred: h.appendDeferred(deferredCall{attrs: attrs})}
}

// WithGroup сохраняет обёртку по той же причине, что и WithAttrs.
func (h contextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return contextHandler{Handler: h.Handler, deferred: h.appendDeferred(deferredCall{group: name})}
}

// appendDeferred добавляет вызов, не задевая уже выданные обработчики:
// у логгера, от которого ответвились двое, общий массив был бы переписан
// вторым ответвлением.
func (h contextHandler) appendDeferred(call deferredCall) []deferredCall {
	return append(slices.Clip(h.deferred), call)
}
