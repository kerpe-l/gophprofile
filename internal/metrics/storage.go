package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// StorageCollector отдаёт объём живых аватаров, читая сумму из БД
// на каждый scrape.
type StorageCollector struct {
	bytes func(ctx context.Context) (int64, error)
	// timeout — предел на чтение суммы: scrape не должен ждать дольше
	// своего интервала.
	timeout time.Duration
	desc    *prometheus.Desc
	log     *slog.Logger
}

// NewStorageCollector создаёт коллектор поверх источника суммы.
func NewStorageCollector(
	bytes func(ctx context.Context) (int64, error),
	timeout time.Duration,
	log *slog.Logger,
) *StorageCollector {
	return &StorageCollector{
		bytes:   bytes,
		timeout: timeout,
		desc: prometheus.NewDesc("avatars_storage_bytes",
			"Total size of live avatar originals.", nil, nil),
		log: log,
	}
}

// Describe отдаёт описание метрики коллектора.
func (c *StorageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect читает сумму и отдаёт гейджу. При недоступном источнике метрика
// в выдачу scrape не попадает, ошибка уходит в лог.
func (c *StorageCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	total, err := c.bytes(ctx)
	if err != nil {
		c.log.Warn("collect storage bytes", slog.Any("error", err))

		return
	}

	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(total))
}
