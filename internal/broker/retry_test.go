package broker

import (
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

// death — запись истории отклонений в том виде, в каком её ведёт брокер.
func death(queue string, count int64) amqp.Table {
	return amqp.Table{"queue": queue, "reason": "expired", countField: count}
}

func TestAttemptNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{
			name:    "first delivery",
			headers: nil,
			want:    1,
		},
		{
			name:    "no death header",
			headers: amqp.Table{"x-first-death-queue": queueProcess},
			want:    1,
		},
		{
			name:    "single retry",
			headers: amqp.Table{argDeath: []any{death("avatars.retry.5s", 1)}},
			want:    2,
		},
		{
			// История в заголовке ведётся по очередям, и у каждой свой счётчик.
			name: "several queues",
			headers: amqp.Table{argDeath: []any{
				death("avatars.retry.5s", 1),
				death("avatars.retry.30s", 1),
				death("avatars.retry.5m", 2),
			}},
			want: 5,
		},
		{
			// Свой счётчик главнее истории отклонений: брокер собирает её
			// заново на каждой очереди и показывает по ней одно отклонение,
			// сколько бы повторов сообщение ни пережило.
			name: "own counter wins",
			headers: amqp.Table{
				headerAttempts: int64(3),
				argDeath:       []any{death("avatars.retry.5m", 1)},
			},
			want: 4,
		},
		{
			name:    "own counter of unexpected type",
			headers: amqp.Table{headerAttempts: "3"},
			want:    1,
		},
		{
			name:    "unexpected header type",
			headers: amqp.Table{argDeath: "not a list"},
			want:    1,
		},
		{
			name:    "unexpected entry type",
			headers: amqp.Table{argDeath: []any{"not a table"}},
			want:    1,
		},
		{
			name:    "unexpected count type",
			headers: amqp.Table{argDeath: []any{amqp.Table{countField: "1"}}},
			want:    1,
		},
		{
			name:    "negative count",
			headers: amqp.Table{argDeath: []any{death(queueProcess, -3)}},
			want:    1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, attemptNumber(tc.headers))
		})
	}
}

func TestIsFinalAttempt(t *testing.T) {
	t.Parallel()

	assert.False(t, isFinalAttempt(maxAttempts-1), "попытки ещё остались")
	assert.True(t, isFinalAttempt(maxAttempts), "пятая попытка последняя")
	assert.True(t, isFinalAttempt(maxAttempts+1))
}

func TestRetryLevelFor(t *testing.T) {
	t.Parallel()

	levels := newRetryLevels(defaultRetryDelays())

	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{name: "first", attempt: 1, want: 5 * time.Second},
		{name: "second", attempt: 2, want: 30 * time.Second},
		{name: "third", attempt: 3, want: 5 * time.Minute},
		{name: "beyond the ladder", attempt: maxAttempts, want: 5 * time.Minute},
		{name: "below the ladder", attempt: 0, want: 5 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, retryLevelFor(levels, tc.attempt).ttl)
		})
	}
}

func TestNewRetryLevels(t *testing.T) {
	t.Parallel()

	levels := newRetryLevels(defaultRetryDelays())

	queues := make([]string, 0, len(levels))
	keys := make([]string, 0, len(levels))

	for _, level := range levels {
		queues = append(queues, level.queue)
		keys = append(keys, level.key)
	}

	assert.Equal(t, []string{"avatars.retry.5s", "avatars.retry.30s", "avatars.retry.5m"}, queues)
	assert.Equal(t, []string{"retry.5s", "retry.30s", "retry.5m"}, keys)
}

func TestDelayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		delay time.Duration
		want  string
	}{
		{name: "seconds", delay: 5 * time.Second, want: "5s"},
		{name: "tens of seconds", delay: 30 * time.Second, want: "30s"},
		{name: "minutes", delay: 5 * time.Minute, want: "5m"},
		{name: "milliseconds", delay: 200 * time.Millisecond, want: "200ms"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, delayName(tc.delay))
		})
	}
}
