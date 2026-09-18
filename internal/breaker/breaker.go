// Package breaker оборачивает зависимости сервисного слоя в circuit breaker:
// после серии подряд идущих отказов вызовы отклоняются сразу, без ожидания
// таймаута зависимости, пробный вызов — по истечении окна. Отклонённый вызов
// возвращает domain.ErrUnavailable.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// Порог открытия и длительность открытого состояния.
const (
	consecutiveFailures = 5
	openTimeout         = 30 * time.Second
)

// newBreaker собирает breaker с общими настройками и логом смены состояния.
func newBreaker(name string, log *slog.Logger) *gobreaker.CircuitBreaker[any] {
	return gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:    name,
		Timeout: openTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= consecutiveFailures
		},
		// Отсутствие объекта и отмена клиентом — не отказ зависимости.
		IsSuccessful: func(err error) bool {
			return err == nil ||
				errors.Is(err, domain.ErrNotFound) ||
				errors.Is(err, context.Canceled)
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			log.Warn("circuit breaker state changed",
				slog.String("breaker", name),
				slog.String("from", from.String()),
				slog.String("to", to.String()),
			)
		},
	})
}

// execute проводит вызов через breaker, переводя отказ самого breaker'а
// в domain.ErrUnavailable; ошибки зависимости уходят как есть.
func execute(cb *gobreaker.CircuitBreaker[any], name string, fn func() (any, error)) (any, error) {
	v, err := cb.Execute(fn)
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return v, fmt.Errorf("%s: %w", name, domain.ErrUnavailable)
	}

	return v, err
}

// Имена breaker'ов в логах и текстах ошибок.
const (
	nameStorage   = "storage"
	namePublisher = "publisher"
)

// Storage защищает объектное хранилище. Методы делят один breaker:
// отказ хранилища для них общий.
type Storage struct {
	inner service.Storage
	cb    *gobreaker.CircuitBreaker[any]
}

// NewStorage оборачивает хранилище в breaker.
func NewStorage(inner service.Storage, log *slog.Logger) *Storage {
	return &Storage{inner: inner, cb: newBreaker(nameStorage, log)}
}

// Put кладёт объект через breaker.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := execute(s.cb, nameStorage, func() (any, error) {
		return nil, s.inner.Put(ctx, key, r, size, contentType)
	})

	return err
}

// Get открывает объект через breaker.
func (s *Storage) Get(ctx context.Context, key string) (domain.StoredObject, error) {
	v, err := execute(s.cb, nameStorage, func() (any, error) {
		return s.inner.Get(ctx, key)
	})
	if err != nil {
		return domain.StoredObject{}, err
	}

	obj, ok := v.(domain.StoredObject)
	if !ok {
		return domain.StoredObject{}, fmt.Errorf("%s: unexpected result type %T", nameStorage, v)
	}

	return obj, nil
}

// DeleteMany удаляет объекты через breaker.
func (s *Storage) DeleteMany(ctx context.Context, keys []string) error {
	_, err := execute(s.cb, nameStorage, func() (any, error) {
		return nil, s.inner.DeleteMany(ctx, keys)
	})

	return err
}

// Publisher защищает публикацию событий.
type Publisher struct {
	inner service.Publisher
	cb    *gobreaker.CircuitBreaker[any]
}

// NewPublisher оборачивает публикатора в breaker.
func NewPublisher(inner service.Publisher, log *slog.Logger) *Publisher {
	return &Publisher{inner: inner, cb: newBreaker(namePublisher, log)}
}

// Publish публикует событие через breaker.
func (p *Publisher) Publish(ctx context.Context, event broker.Event) error {
	_, err := execute(p.cb, namePublisher, func() (any, error) {
		return nil, p.inner.Publish(ctx, event)
	})

	return err
}
