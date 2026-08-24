package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// httpBuckets — бакеты длительности HTTP-запроса.
func httpBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 180}
}

// HTTP — RED-метрики HTTP-сервера.
type HTTP struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewHTTP создаёт и регистрирует RED-метрики.
func NewHTTP(reg *prometheus.Registry) *HTTP {
	m := &HTTP{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Handled HTTP requests.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request handling duration.",
			Buckets: httpBuckets(),
		}, []string{"method", "route"}),
	}

	reg.MustRegister(m.requests, m.duration)

	return m
}

// Observe учитывает один обработанный запрос. route — route pattern роутера,
// а не сырой путь: значения лейбла обязаны быть перечислимыми.
func (m *HTTP) Observe(method, route string, status int, took time.Duration) {
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(method, route).Observe(took.Seconds())
}
