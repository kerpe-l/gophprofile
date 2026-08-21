package observability

import (
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// SpanError записывает ошибку в спан и помечает его статусом Error.
// Возвращает её же, чтобы вписываться прямо в return.
func SpanError(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())

	return err
}
