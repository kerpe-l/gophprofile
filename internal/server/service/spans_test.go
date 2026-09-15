package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
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
func TestServiceSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	otel.SetTracerProvider(provider)
	// Остановленный провайдер роняет спаны: тесты после этого в recorder
	// не пишут.
	t.Cleanup(func() { _ = provider.Shutdown(context.WithoutCancel(t.Context())) })

	d := newDeps()
	svc := newService(t, d)

	id := uuid.New()
	d.repo.avatar = domain.Avatar{ID: id, UserID: "user-1"}

	_, err := svc.Metadata(t.Context(), id)
	require.NoError(t, err)

	d.repo.getErr = errors.New("database is down")

	_, err = svc.Metadata(t.Context(), id)
	require.Error(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 2)

	okSpan, errSpan := spans[0], spans[1]

	assert.Equal(t, "service.metadata", okSpan.Name())
	assert.Contains(t, okSpan.Attributes(), attribute.String("avatar_id", id.String()))
	assert.Equal(t, codes.Unset, okSpan.Status().Code)

	assert.Equal(t, "service.metadata", errSpan.Name())
	assert.Equal(t, codes.Error, errSpan.Status().Code)
	// RecordError оставляет событие exception с текстом ошибки.
	require.NotEmpty(t, errSpan.Events())
	assert.Equal(t, "exception", errSpan.Events()[0].Name)
}
