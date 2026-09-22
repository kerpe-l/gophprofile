package http_test

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	serverhttp "github.com/kerpe-l/gophprofile/internal/server/http"
)

func newLimitedRouter(t *testing.T, svc *fakeService, limit config.RateLimit) http.Handler {
	t.Helper()

	return serverhttp.New(serverhttp.Deps{
		Service: svc,
		HTTP: config.HTTP{
			ReadTimeout:    time.Minute,
			RequestTimeout: time.Minute,
		},
		RateLimit:      limit,
		MaxUploadBytes: testMaxBytes,
		Log:            slog.New(slog.DiscardHandler),
	})
}

func TestRateLimiting(t *testing.T) {
	t.Parallel()

	svc := &fakeService{avatar: completedAvatar()}
	router := newLimitedRouter(t, svc, config.RateLimit{RPS: 0.001, Burst: 1})
	path := "/api/v1/avatars/" + completedAvatar().ID.String() + "/metadata"

	w := do(t, router, request(t, http.MethodGet, path))
	require.Equal(t, http.StatusOK, w.Code)

	w = do(t, router, request(t, http.MethodGet, path))
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.Contains(t, w.Body.String(), "Too many requests")
	// Отклонённый запрос до сервиса не доходит.
	assert.Equal(t, 1, svc.gotRequests)

	// Пробы под ограничитель не попадают.
	for range 3 {
		w = do(t, router, request(t, http.MethodGet, "/livez"))
		assert.Equal(t, http.StatusOK, w.Code)
	}
}

func TestRateLimitingDisabled(t *testing.T) {
	t.Parallel()

	router := newLimitedRouter(t, &fakeService{avatar: completedAvatar()}, config.RateLimit{})
	path := "/api/v1/avatars/" + completedAvatar().ID.String() + "/metadata"

	for range 20 {
		w := do(t, router, request(t, http.MethodGet, path))
		require.Equal(t, http.StatusOK, w.Code)
	}
}
