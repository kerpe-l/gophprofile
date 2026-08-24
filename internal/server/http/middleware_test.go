package http_test

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/metrics"
	serverhttp "github.com/kerpe-l/gophprofile/internal/server/http"
)

// Идентификатор запроса доходит и до клиента, и до сервиса: по нему
// связываются ответ и записи в логе.
func TestRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		incoming string
		wantSame bool
	}{
		{name: "from client", incoming: "abc-123", wantSame: true},
		{name: "absent", incoming: "", wantSame: false},
		{name: "too long", incoming: strings.Repeat("x", 65), wantSame: false},
		{name: "not printable", incoming: "id\nwith-newline", wantSame: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{avatar: completedAvatar()}
			router := newRouter(t, svc, nil)

			r := request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"/metadata")
			if tc.incoming != "" {
				r.Header.Set("X-Request-ID", tc.incoming)
			}

			w := do(t, router, r)

			id := w.Header().Get("X-Request-ID")
			require.NotEmpty(t, id)
			assert.Equal(t, id, requestIDOf(svc.gotContext))

			if tc.wantSame {
				assert.Equal(t, tc.incoming, id)
			} else {
				assert.NotEqual(t, tc.incoming, id)
			}
		})
	}
}

func TestRecovering(t *testing.T) {
	t.Parallel()

	svc := &fakeService{panics: true}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"/metadata"))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "Internal error")
	// Наружу не уходит ни текст паники, ни трассировка.
	assert.NotContains(t, w.Body.String(), "metadata exploded")
}

func TestUnknownRoute(t *testing.T) {
	t.Parallel()

	router := newRouter(t, &fakeService{}, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/unknown"))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// В лейбле route — pattern роутера, а не сырой путь: иначе каждый
// идентификатор аватара дал бы новую серию.
func TestMeasuring(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	router := newMeasuredRouter(t, &fakeService{avatar: completedAvatar()}, reg)

	do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"/metadata"))
	do(t, router, request(t, http.MethodGet, "/nowhere"))
	do(t, router, request(t, http.MethodGet, "/health"))

	w := do(t, router, request(t, http.MethodGet, "/metrics"))
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.Contains(t, body,
		`http_requests_total{method="GET",route="/api/v1/avatars/{avatar_id}/metadata",status="200"} 1`)
	assert.Contains(t, body,
		`http_requests_total{method="GET",route="unmatched",status="404"} 1`)
	// Health и сам scrape в RED не попадают.
	assert.NotContains(t, body, `route="/health"`)
	assert.NotContains(t, body, `route="/metrics"`)
}

// newMeasuredRouter — роутер с включёнными RED-метриками и маршрутом /metrics.
func newMeasuredRouter(t *testing.T, svc *fakeService, reg *prometheus.Registry) http.Handler {
	t.Helper()

	return serverhttp.New(serverhttp.Deps{
		Service: svc,
		HTTP: config.HTTP{
			ReadTimeout:    time.Minute,
			RequestTimeout: time.Minute,
		},
		MaxUploadBytes: testMaxBytes,
		Metrics:        metrics.NewHTTP(reg),
		MetricsHandler: metrics.Handler(reg),
		Log:            slog.New(slog.DiscardHandler),
	})
}
