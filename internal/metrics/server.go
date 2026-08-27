package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Server — бизнес-метрики API: загрузки и удаления аватаров.
type Server struct {
	uploads        *prometheus.CounterVec
	uploadDuration *prometheus.HistogramVec
	deletes        *prometheus.CounterVec
}

// NewServer создаёт и регистрирует бизнес-метрики API.
func NewServer(reg *prometheus.Registry) *Server {
	m := &Server{
		uploads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_uploads_total",
			Help: "Avatar upload attempts.",
		}, []string{"status"}),
		uploadDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "avatars_upload_duration_seconds",
			Help:    "Avatar upload duration.",
			Buckets: operationBuckets(),
		}, []string{"status"}),
		deletes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_deletes_total",
			Help: "Avatar delete attempts.",
		}, []string{"status"}),
	}

	reg.MustRegister(m.uploads, m.uploadDuration, m.deletes)

	return m
}

// ObserveUpload учитывает одну попытку загрузки.
func (m *Server) ObserveUpload(success bool, took time.Duration) {
	s := status(success)
	m.uploads.WithLabelValues(s).Inc()
	m.uploadDuration.WithLabelValues(s).Observe(took.Seconds())
}

// IncDelete учитывает одну попытку удаления.
func (m *Server) IncDelete(success bool) {
	m.deletes.WithLabelValues(status(success)).Inc()
}
