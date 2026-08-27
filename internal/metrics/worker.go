package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Worker — метрики обработки аватаров воркером.
type Worker struct {
	processing         *prometheus.CounterVec
	processingDuration *prometheus.HistogramVec
}

// NewWorker создаёт и регистрирует метрики обработки.
func NewWorker(reg *prometheus.Registry) *Worker {
	m := &Worker{
		processing: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_processing_total",
			Help: "Avatar processing attempts.",
		}, []string{"status"}),
		processingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "avatars_processing_duration_seconds",
			Help:    "Avatar processing duration.",
			Buckets: operationBuckets(),
		}, []string{"status"}),
	}

	reg.MustRegister(m.processing, m.processingDuration)

	return m
}

// ObserveProcessing учитывает одну попытку обработки любого исхода.
func (m *Worker) ObserveProcessing(success bool, took time.Duration) {
	s := status(success)
	m.processing.WithLabelValues(s).Inc()
	m.processingDuration.WithLabelValues(s).Observe(took.Seconds())
}
