package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const headerTraceparent = "traceparent"

func TestHeaderCarrier(t *testing.T) {
	t.Parallel()

	headers := amqp.Table{
		headerTraceparent: "00-aa-bb-01",
		headerAttempts:    int64(3),
	}
	carrier := headerCarrier(headers)

	assert.Equal(t, "00-aa-bb-01", carrier.Get(headerTraceparent))
	// Значение-нестрока неотличимо от отсутствующего.
	assert.Empty(t, carrier.Get(headerAttempts))
	assert.Empty(t, carrier.Get("missing"))
	assert.ElementsMatch(t, []string{headerTraceparent, headerAttempts}, carrier.Keys())

	carrier.Set("tracestate", "vendor=1")
	assert.Equal(t, "vendor=1", carrier.Get("tracestate"))
}

// setupPropagator ставит W3C-propagator вместо глобального noop. После теста
// ставится пустой composite: исходный глобальный объект вернуть нельзя —
// SetTextMapPropagator отказывается от самоделегирования.
func setupPropagator(t *testing.T) {
	t.Helper()

	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator()) })
}

// spanContext собирает валидный контекст спана с фиксированными
// идентификаторами.
func spanContext(t *testing.T) trace.SpanContext {
	t.Helper()

	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01},
		SpanID:     trace.SpanID{0x02},
		TraceFlags: trace.FlagsSampled,
	})
}

//nolint:paralleltest // тест подменяет глобальный propagator
func TestNewPublishingTraceContext(t *testing.T) {
	setupPropagator(t)

	event := NewUploadEvent(uuid.New(), "user-1", "avatars/key")

	sc := spanContext(t)

	msg, err := newPublishing(trace.ContextWithSpanContext(t.Context(), sc), event)
	require.NoError(t, err)

	traceparent, ok := msg.Headers[headerTraceparent].(string)
	require.True(t, ok, "traceparent header is missing")
	assert.Contains(t, traceparent, sc.TraceID().String())

	// Без активного спана заголовок не пишется вовсе.
	msg, err = newPublishing(t.Context(), event)
	require.NoError(t, err)
	assert.NotContains(t, msg.Headers, headerTraceparent)
}

// fakeAcknowledger подтверждает доставки без брокера.
type fakeAcknowledger struct {
	acked  bool
	ackErr error
}

func (a *fakeAcknowledger) Ack(uint64, bool) error { a.acked = true; return a.ackErr }

func (a *fakeAcknowledger) Nack(uint64, bool, bool) error { return nil }

func (a *fakeAcknowledger) Reject(uint64, bool) error { return nil }

//nolint:paralleltest // тест подменяет глобальный propagator
func TestProcessConsumerSpan(t *testing.T) {
	setupPropagator(t)

	tests := []struct {
		name       string
		handlerErr error
		wantStatus codes.Code
	}{
		{
			name:       "success",
			handlerErr: nil,
			wantStatus: codes.Unset,
		},
		{
			name:       "non-retryable failure",
			handlerErr: fmt.Errorf("broken payload: %w", ErrNonRetryable),
			wantStatus: codes.Error,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

			c := &Consumer{
				log:     slog.New(slog.DiscardHandler),
				tracer:  provider.Tracer(tracerName),
				levels:  newRetryLevels(defaultRetryDelays()),
				timeout: time.Minute,
			}

			parent := spanContext(t)
			headers := amqp.Table{}
			propagation.TraceContext{}.Inject(
				trace.ContextWithSpanContext(t.Context(), parent), headerCarrier(headers))

			ack := &fakeAcknowledger{}
			delivery := amqp.Delivery{
				Acknowledger: ack,
				Headers:      headers,
				Type:         RoutingKeyUploaded,
				MessageId:    "msg-1",
				Body:         []byte("{}"),
			}

			c.process(t.Context(), func(context.Context, Message) error {
				return tc.handlerErr
			}, delivery)

			assert.True(t, ack.acked)

			spans := recorder.Ended()
			require.Len(t, spans, 1)

			span := spans[0]
			assert.Equal(t, "process "+RoutingKeyUploaded, span.Name())
			assert.Equal(t, trace.SpanKindConsumer, span.SpanKind())
			// Спан продолжает трейс, приехавший в заголовках сообщения.
			assert.Equal(t, parent.TraceID(), span.SpanContext().TraceID())
			assert.Equal(t, parent.SpanID(), span.Parent().SpanID())
			assert.Equal(t, tc.wantStatus, span.Status().Code)
		})
	}
}

// Неподтверждённое сообщение будет доставлено повторно, поэтому успешная
// обработка с проваленным Ack не выглядит в трейсе успехом.
//
//nolint:paralleltest // тест подменяет глобальный propagator
func TestProcessAckFailureInSpan(t *testing.T) {
	setupPropagator(t)

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	c := &Consumer{
		log:     slog.New(slog.DiscardHandler),
		tracer:  provider.Tracer(tracerName),
		levels:  newRetryLevels(defaultRetryDelays()),
		timeout: time.Minute,
	}

	delivery := amqp.Delivery{
		Acknowledger: &fakeAcknowledger{ackErr: errors.New("channel closed")},
		Type:         RoutingKeyUploaded,
		MessageId:    "msg-1",
		Body:         []byte("{}"),
	}

	c.process(t.Context(), func(context.Context, Message) error { return nil }, delivery)

	spans := recorder.Ended()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Contains(t, span.Attributes(), attribute.String("outcome", "ack_failed"))
}
