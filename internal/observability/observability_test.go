package observability_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

func TestSetupDisabled(t *testing.T) {
	t.Parallel()

	p, err := observability.Setup(t.Context(), config.Otel{}, "test", "local", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(t.Context()))
}

// Соединение с коллектором ленивое: недоступный endpoint не мешает ни
// старту, ни завершению без накопленных спанов.
func TestSetupUnreachableEndpoint(t *testing.T) {
	t.Parallel()

	cfg := config.Otel{Endpoint: "127.0.0.1:1", Insecure: true, SampleRatio: 1}

	p, err := observability.Setup(t.Context(), cfg, "test", "local", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	require.NoError(t, p.Shutdown(ctx))
}
