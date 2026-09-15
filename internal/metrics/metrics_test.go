package metrics_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/metrics"
)

// counterValue находит значение счётчика с данными лейблами в выдаче регистра.
// Отсутствующая серия — ноль: счётчик, который ни разу не инкрементили,
// в выдаче не появляется.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != name {
			continue
		}

	metric:
		for _, m := range family.GetMetric() {
			for _, pair := range m.GetLabel() {
				if labels[pair.GetName()] != pair.GetValue() {
					continue metric
				}
			}

			return m.GetCounter().GetValue()
		}
	}

	return 0
}

func TestHTTPObserve(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	m := metrics.NewHTTP(reg)

	m.Observe(http.MethodGet, "/api/v1/avatars/{avatar_id}", http.StatusOK, 10*time.Millisecond)
	m.Observe(http.MethodGet, "/api/v1/avatars/{avatar_id}", http.StatusOK, 20*time.Millisecond)
	m.Observe(http.MethodPost, "/api/v1/avatars", http.StatusInternalServerError, time.Millisecond)

	assert.InDelta(t, 2, counterValue(t, reg, "http_requests_total", map[string]string{
		"method": http.MethodGet, "route": "/api/v1/avatars/{avatar_id}", "status": "200",
	}), 1e-9)
	assert.InDelta(t, 1, counterValue(t, reg, "http_requests_total", map[string]string{
		"method": http.MethodPost, "route": "/api/v1/avatars", "status": "500",
	}), 1e-9)
}

func TestServerMetrics(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	m := metrics.NewServer(reg)

	m.ObserveUpload(true, time.Second)
	m.ObserveUpload(false, time.Second)
	m.ObserveUpload(false, time.Second)
	m.IncDelete(true)
	m.IncDelete(false)

	assert.InDelta(t, 1, counterValue(t, reg, "avatars_uploads_total", map[string]string{"status": "success"}), 1e-9)
	assert.InDelta(t, 2, counterValue(t, reg, "avatars_uploads_total", map[string]string{"status": "error"}), 1e-9)
	assert.InDelta(t, 1, counterValue(t, reg, "avatars_deletes_total", map[string]string{"status": "success"}), 1e-9)
	assert.InDelta(t, 1, counterValue(t, reg, "avatars_deletes_total", map[string]string{"status": "error"}), 1e-9)
}

func TestWorkerMetrics(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	m := metrics.NewWorker(reg)

	m.ObserveProcessing(true, time.Second)
	m.ObserveProcessing(false, 2*time.Second)

	assert.InDelta(t, 1, counterValue(t, reg, "avatars_processing_total", map[string]string{"status": "success"}), 1e-9)
	assert.InDelta(t, 1, counterValue(t, reg, "avatars_processing_total", map[string]string{"status": "error"}), 1e-9)
}

// Пул не подключается к базе: статистика снимается и с пула без единого
// соединения.
func TestPoolCollector(t *testing.T) {
	t.Parallel()

	pool, err := pgxpool.New(t.Context(), "postgres://user:pass@localhost:1/none")
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	c := metrics.NewPoolCollector(pool.Stat)

	problems, err := testutil.CollectAndLint(c)
	require.NoError(t, err)
	assert.Empty(t, problems)
	assert.Equal(t, 11, testutil.CollectAndCount(c))
}

func TestStorageCollector(t *testing.T) {
	t.Parallel()

	c := metrics.NewStorageCollector(func(context.Context) (int64, error) {
		return 42, nil
	}, time.Second, slog.New(slog.DiscardHandler))

	expected := `
# HELP avatars_storage_bytes Total size of live avatar originals.
# TYPE avatars_storage_bytes gauge
avatars_storage_bytes 42
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected)))
}

// Недоступный источник не валит scrape: метрика просто выпадает из выдачи.
func TestStorageCollectorSourceFails(t *testing.T) {
	t.Parallel()

	c := metrics.NewStorageCollector(func(context.Context) (int64, error) {
		return 0, errors.New("database is down")
	}, time.Second, slog.New(slog.DiscardHandler))

	assert.Zero(t, testutil.CollectAndCount(c))
}

func TestHandlerServesRegistry(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	metrics.NewHTTP(reg).Observe(http.MethodGet, "/health", http.StatusOK, time.Millisecond)

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)

	metrics.Handler(reg).ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "http_requests_total")
	assert.Contains(t, w.Body.String(), "go_goroutines")
}
