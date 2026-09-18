package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
)

// fakeClock — управляемое время бакетов.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func newTestLimiters(cfg config.RateLimit) (*limiters, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := newLimiters(cfg)
	l.now = func() time.Time { return clock.now }

	return l, clock
}

func TestLimitersBurstAndRefill(t *testing.T) {
	t.Parallel()

	l, clock := newTestLimiters(config.RateLimit{RPS: 1, Burst: 2})

	for range 2 {
		ok, _ := l.allow("alice")
		require.True(t, ok)
	}

	ok, retryAfter := l.allow("alice")
	require.False(t, ok)
	assert.Equal(t, 1, retryAfter)

	// Отказ не тратит будущий токен: через секунду запрос снова проходит.
	clock.advance(time.Second)

	ok, _ = l.allow("alice")
	assert.True(t, ok)
}

func TestLimitersKeysIndependent(t *testing.T) {
	t.Parallel()

	l, _ := newTestLimiters(config.RateLimit{RPS: 1, Burst: 1})

	ok, _ := l.allow("alice")
	require.True(t, ok)

	ok, _ = l.allow("alice")
	require.False(t, ok)

	ok, _ = l.allow("bob")
	assert.True(t, ok)
}

func TestLimitersDefaultBurst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rps       float64
		wantBurst int
	}{
		{name: "twice rps", rps: 5, wantBurst: 10},
		{name: "fraction rounds up", rps: 0.75, wantBurst: 2},
		{name: "at least one", rps: 0.1, wantBurst: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.wantBurst, newLimiters(config.RateLimit{RPS: tc.rps}).burst)
		})
	}
}

func TestLimitersSweepIdleKeys(t *testing.T) {
	t.Parallel()

	l, clock := newTestLimiters(config.RateLimit{RPS: 1, Burst: 1})

	l.allow("idle")
	clock.advance(limiterIdleTTL / 2)
	l.allow("active")

	clock.advance(limiterIdleTTL/2 + time.Second)
	l.allow("active")

	assert.NotContains(t, l.visitors, "idle")
	assert.Contains(t, l.visitors, "active")
}

func TestLimitKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		userID string
		remote string
		want   string
	}{
		{name: "user header", userID: "alice", remote: "10.0.0.1:5000", want: "alice"},
		{name: "no header", remote: "10.0.0.1:5000", want: "10.0.0.1"},
		{name: "header too long", userID: string(make([]byte, maxUserIDLen+1)), remote: "10.0.0.1:5000", want: "10.0.0.1"},
		{name: "remote without port", remote: "10.0.0.1", want: "10.0.0.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote

			if tc.userID != "" {
				r.Header.Set(headerUserID, tc.userID)
			}

			assert.Equal(t, tc.want, limitKey(r))
		})
	}
}
