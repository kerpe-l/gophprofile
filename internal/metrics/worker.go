package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Worker — метрики обработки аватаров воркером.
type Worker struct {
	processing         *prometheus.CounterVec
	processingDuration prometheus.Histogram
}

// NewWorker создаёт и регистрирует метрики обработки.
func NewWorker(reg *prometheus.Registry) *Worker {
	m := &Worker{
		processing: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatars_processing_total",
			Help: "Avatar processing attempts.",
		}, []string{"status"}),
		processingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "avatars_processing_duration_seconds",
			Help:    "Avatar processing duration.",
			Buckets: operationBuckets(),
		}),
	}

	reg.MustRegister(m.processing, m.processingDuration)

	return m
}

// ObserveProcessing учитывает одну попытку обработки любого исхода.
func (m *Worker) ObserveProcessing(success bool, took time.Duration) {
	m.processing.WithLabelValues(status(success)).Inc()
	m.processingDuration.Observe(took.Seconds())
}
