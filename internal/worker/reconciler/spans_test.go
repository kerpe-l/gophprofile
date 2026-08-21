package reconciler_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

//nolint:paralleltest // тест ставит глобальный провайдер трейсинга
func TestReconcileSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	otel.SetTracerProvider(provider)
	// Остановленный провайдер роняет спаны: тесты после этого в recorder
	// не пишут.
	t.Cleanup(func() { _ = provider.Shutdown(context.WithoutCancel(t.Context())) })

	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{passes: []pass{{avatars: []domain.Avatar{stuckAvatar()}}}}
		pub := &fakePublisher{}

		stop := run(t, repo, pub)
		defer stop()

		tick(t)

		spans := recorder.Ended()
		require.Len(t, spans, 1)

		span := spans[0]
		assert.Equal(t, "reconcile stuck uploads", span.Name())
		// Спан корневой: у прохода по тикеру нет входящего контекста.
		assert.False(t, span.Parent().IsValid())
		assert.Contains(t, span.Attributes(), attribute.Int("republished", 1))
		assert.Equal(t, codes.Unset, span.Status().Code)
	})
}
