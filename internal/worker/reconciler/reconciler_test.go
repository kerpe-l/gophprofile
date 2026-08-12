package reconciler_test

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/worker/reconciler"
)

// Параметры добора в тестах: время виртуальное, поэтому значения взяты
// боевые, а ожидание всё равно мгновенное.
const (
	interval   = time.Minute
	stuckAfter = 5 * time.Minute
	batch      = 10
)

var errUnavailable = errors.New("service unavailable")

// pass — результат одного прохода: что отдаёт перебор зависших записей.
type pass struct {
	avatars []domain.Avatar
	err     error
}

// fakeRepo отдаёт по проходу за вызов; лишние вызовы получают пустой перебор.
type fakeRepo struct {
	mu     sync.Mutex
	passes []pass

	// befores — отсечки, с которыми звали перебор.
	befores []time.Time
	limits  []int
}

func (r *fakeRepo) SelectStuck(_ context.Context, before time.Time, limit int) iter.Seq2[domain.Avatar, error] {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.befores = append(r.befores, before)
	r.limits = append(r.limits, limit)

	var current pass

	if len(r.passes) > 0 {
		current, r.passes = r.passes[0], r.passes[1:]
	}

	return func(yield func(domain.Avatar, error) bool) {
		for _, avatar := range current.avatars {
			if !yield(avatar, nil) {
				return
			}
		}

		if current.err != nil {
			yield(domain.Avatar{}, current.err)
		}
	}
}

// stats возвращает отсечки и пределы, с которыми звали перебор.
func (r *fakeRepo) stats() (befores []time.Time, limits []int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]time.Time(nil), r.befores...), append([]int(nil), r.limits...)
}

// fakePublisher запоминает опубликованные события.
type fakePublisher struct {
	mu sync.Mutex

	err    error
	events []broker.Event
}

// fail задаёт исход следующих публикаций.
func (p *fakePublisher) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.err = err
}

func (p *fakePublisher) Publish(_ context.Context, event broker.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.err != nil {
		return p.err
	}

	p.events = append(p.events, event)

	return nil
}

func (p *fakePublisher) published() []broker.Event {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]broker.Event(nil), p.events...)
}

func stuckAvatar() domain.Avatar {
	id := uuid.New()

	return domain.Avatar{
		ID:               id,
		UserID:           "user-1",
		S3Key:            domain.OriginalKey(id),
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

// run запускает добор в отдельной горутине и возвращает функцию остановки,
// дожидающуюся его завершения.
func run(t *testing.T, repo *fakeRepo, pub *fakePublisher) func() {
	t.Helper()

	r := reconciler.New(repo, pub, reconciler.Config{
		Interval:   interval,
		StuckAfter: stuckAfter,
		Batch:      batch,
	}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())

	var wg sync.WaitGroup

	wg.Go(func() {
		r.Run(ctx)
	})

	return func() {
		cancel()
		wg.Wait()
	}
}

// tick прокручивает виртуальное время на один период и дожидается, пока проход
// закончится: под synctest ожидание мгновенно, а Wait возвращается, когда все
// горутины пузыря заблокированы.
func tick(t *testing.T) {
	t.Helper()

	time.Sleep(interval)
	synctest.Wait()
}

func TestReconcilerRepublishesStuckUploads(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		avatar := stuckAvatar()
		repo := &fakeRepo{passes: []pass{{avatars: []domain.Avatar{avatar}}}}
		pub := &fakePublisher{}

		stop := run(t, repo, pub)
		defer stop()

		// До первого тика добор не трогает базу вовсе.
		synctest.Wait()

		befores, _ := repo.stats()
		assert.Empty(t, befores)

		tick(t)

		events := pub.published()
		require.Len(t, events, 1)

		event, ok := events[0].(broker.AvatarUploadEvent)
		require.True(t, ok)
		assert.Equal(t, avatar.ID.String(), event.AvatarID)
		assert.Equal(t, avatar.UserID, event.UserID)
		assert.Equal(t, avatar.S3Key, event.S3Key)
		assert.Equal(t, broker.RoutingKeyUploaded, event.RoutingKey())
		assert.NotEmpty(t, event.ID())

		// Отбираются записи старше отсечки, а не свежие.
		befores, limits := repo.stats()
		require.Len(t, befores, 1)
		assert.Equal(t, time.Now().Add(-stuckAfter), befores[0])
		assert.Equal(t, []int{batch}, limits)
	})
}

func TestReconcilerPublishesNothingWithoutStuckUploads(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		pub := &fakePublisher{}

		stop := run(t, repo, pub)
		defer stop()

		tick(t)
		tick(t)

		assert.Empty(t, pub.published())

		befores, _ := repo.stats()
		assert.Len(t, befores, 2)
	})
}

// Неудачный проход цикл не останавливает: зависшие загрузки подбираются
// на следующем тике.
func TestReconcilerSurvivesFailedPass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*fakeRepo, *fakePublisher)
	}{
		{
			name: "select fails",
			setup: func(repo *fakeRepo, _ *fakePublisher) {
				repo.passes[0] = pass{err: errUnavailable}
			},
		},
		{
			name: "publish fails",
			setup: func(_ *fakeRepo, pub *fakePublisher) {
				pub.fail(errUnavailable)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				avatar := stuckAvatar()
				repo := &fakeRepo{passes: []pass{
					{avatars: []domain.Avatar{stuckAvatar()}},
					{avatars: []domain.Avatar{avatar}},
				}}
				pub := &fakePublisher{}

				tc.setup(repo, pub)

				stop := run(t, repo, pub)
				defer stop()

				tick(t)
				assert.Empty(t, pub.published())

				pub.fail(nil)

				tick(t)

				events := pub.published()
				require.Len(t, events, 1)

				event, ok := events[0].(broker.AvatarUploadEvent)
				require.True(t, ok)
				assert.Equal(t, avatar.ID.String(), event.AvatarID)
			})
		})
	}
}

// Отмена контекста завершает цикл: без этого stop не дождался бы горутины
// и тест повис бы.
func TestReconcilerStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		repo := &fakeRepo{}
		pub := &fakePublisher{}

		stop := run(t, repo, pub)

		tick(t)
		stop()

		tick(t)

		befores, _ := repo.stats()
		assert.Len(t, befores, 1, "stopped reconciler must not run another pass")
	})
}
