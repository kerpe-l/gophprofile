package breaker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

var errDown = errors.New("connection refused")

// fakeStorage возвращает заданную ошибку и считает обращения.
type fakeStorage struct {
	err   error
	calls int
}

func (s *fakeStorage) Put(context.Context, string, io.Reader, int64, string) error {
	s.calls++

	return s.err
}

func (s *fakeStorage) Get(context.Context, string) (domain.StoredObject, error) {
	s.calls++

	return domain.StoredObject{Size: 42}, s.err
}

func (s *fakeStorage) DeleteMany(context.Context, []string) error {
	s.calls++

	return s.err
}

type fakePublisher struct {
	err   error
	calls int
}

func (p *fakePublisher) Publish(context.Context, broker.Event) error {
	p.calls++

	return p.err
}

func discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestStorageOpensAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	inner := &fakeStorage{err: errDown}
	s := NewStorage(inner, discard())

	for range consecutiveFailures {
		err := s.Put(t.Context(), "k", nil, 0, "")
		require.ErrorIs(t, err, errDown)
	}

	// Открытый breaker отвечает сразу, до зависимости вызов не доходит.
	_, err := s.Get(t.Context(), "k")
	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Equal(t, consecutiveFailures, inner.calls)
}

func TestStorageNotFoundIsNotFailure(t *testing.T) {
	t.Parallel()

	inner := &fakeStorage{err: domain.ErrNotFound}
	s := NewStorage(inner, discard())

	for range consecutiveFailures * 2 {
		_, err := s.Get(t.Context(), "k")
		require.ErrorIs(t, err, domain.ErrNotFound)
	}

	assert.Equal(t, consecutiveFailures*2, inner.calls)
}

func TestStorageCanceledIsNotFailure(t *testing.T) {
	t.Parallel()

	inner := &fakeStorage{err: context.Canceled}
	s := NewStorage(inner, discard())

	for range consecutiveFailures * 2 {
		err := s.DeleteMany(t.Context(), []string{"k"})
		require.ErrorIs(t, err, context.Canceled)
	}

	assert.Equal(t, consecutiveFailures*2, inner.calls)
}

func TestStorageGetPassesResult(t *testing.T) {
	t.Parallel()

	s := NewStorage(&fakeStorage{}, discard())

	obj, err := s.Get(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, int64(42), obj.Size)
}

// Успех серии не прерывает: счётчик подряд идущих отказов сбрасывается.
func TestStorageSuccessResetsFailures(t *testing.T) {
	t.Parallel()

	inner := &fakeStorage{err: errDown}
	s := NewStorage(inner, discard())

	for range consecutiveFailures - 1 {
		require.ErrorIs(t, s.Put(t.Context(), "k", nil, 0, ""), errDown)
	}

	inner.err = nil

	require.NoError(t, s.Put(t.Context(), "k", nil, 0, ""))

	inner.err = errDown
	for range consecutiveFailures - 1 {
		require.ErrorIs(t, s.Put(t.Context(), "k", nil, 0, ""), errDown)
	}
}

func TestPublisherRecoversAfterTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := &fakePublisher{err: errDown}
		p := NewPublisher(inner, discard())

		for range consecutiveFailures {
			require.ErrorIs(t, p.Publish(t.Context(), broker.NewDeleteEvent(uuid.Nil, nil)), errDown)
		}

		require.ErrorIs(t, p.Publish(t.Context(), broker.NewDeleteEvent(uuid.Nil, nil)), domain.ErrUnavailable)

		// По истечении окна пробный вызов проходит к зависимости и,
		// успешный, закрывает breaker. Окно считается истёкшим строго после
		// своей границы.
		time.Sleep(openTimeout + time.Millisecond)

		inner.err = nil

		require.NoError(t, p.Publish(t.Context(), broker.NewDeleteEvent(uuid.Nil, nil)))
		require.NoError(t, p.Publish(t.Context(), broker.NewDeleteEvent(uuid.Nil, nil)))
		assert.Equal(t, consecutiveFailures+2, inner.calls)
	})
}
