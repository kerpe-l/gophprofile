package broker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleRecoversPanic(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	c := &Consumer{log: slog.New(slog.NewTextHandler(&buf, nil))}

	err := c.handle(t.Context(), func(context.Context, Message) error {
		panic("decoder exploded")
	}, Message{Type: RoutingKeyUploaded, MessageID: "m-1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoder exploded")
	// Паника — повторяемая ошибка: до очереди мёртвых сообщение дойдёт по лестнице.
	require.NotErrorIs(t, err, ErrNonRetryable)
	assert.Contains(t, buf.String(), "panic in handler")
	assert.Contains(t, buf.String(), "m-1")
}

func TestHandlePassesError(t *testing.T) {
	t.Parallel()

	c := &Consumer{log: slog.New(slog.DiscardHandler)}
	want := errors.New("storage down")

	err := c.handle(t.Context(), func(context.Context, Message) error {
		return want
	}, Message{})

	assert.ErrorIs(t, err, want)
}
