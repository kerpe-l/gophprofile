// Package metrics определяет все метрики Prometheus проекта: имена, лейблы
// и бакеты собраны в одном месте, потребители получают готовые типы через
// конструкторы. Регистрация — только в явном Registry, глобальный регистр
// client_golang не используется.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Значения лейбла status бизнес-метрик.
const (
	statusSuccess = "success"
	statusError   = "error"
)

// operationBuckets — бакеты длительности загрузки и обработки: обе операции
// упираются в таймауты в десятки секунд, стандартных бакетов до 10 секунд
// не хватает.
func operationBuckets() []float64 {
	return []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
}

// NewRegistry создаёт регистр со стандартными Go- и process-коллекторами.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return reg
}

// Handler отдаёт содержимое регистра в формате Prometheus.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// status переводит исход операции в значение лейбла.
func status(success bool) string {
	if success {
		return statusSuccess
	}

	return statusError
}
