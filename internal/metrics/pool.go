package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolCollector отдаёт статистику пула pgx на каждый scrape.
type PoolCollector struct {
	stat func() *pgxpool.Stat

	conns             *prometheus.Desc
	maxConns          *prometheus.Desc
	acquires          *prometheus.Desc
	acquireSeconds    *prometheus.Desc
	emptyAcquires     *prometheus.Desc
	canceledAcquires  *prometheus.Desc
	newConns          *prometheus.Desc
	lifetimeDestroyed *prometheus.Desc
	idleDestroyed     *prometheus.Desc
}

// NewPoolCollector создаёт коллектор поверх снимка статистики пула.
func NewPoolCollector(stat func() *pgxpool.Stat) *PoolCollector {
	return &PoolCollector{
		stat: stat,
		conns: prometheus.NewDesc("pgxpool_conns",
			"Current pool connections by state.", []string{"state"}, nil),
		maxConns: prometheus.NewDesc("pgxpool_max_conns",
			"Pool size limit.", nil, nil),
		acquires: prometheus.NewDesc("pgxpool_acquires_total",
			"Successful connection acquires.", nil, nil),
		acquireSeconds: prometheus.NewDesc("pgxpool_acquire_duration_seconds_total",
			"Total time spent acquiring connections.", nil, nil),
		emptyAcquires: prometheus.NewDesc("pgxpool_empty_acquires_total",
			"Acquires that waited for a free connection.", nil, nil),
		canceledAcquires: prometheus.NewDesc("pgxpool_canceled_acquires_total",
			"Acquires canceled by context.", nil, nil),
		newConns: prometheus.NewDesc("pgxpool_new_conns_total",
			"Connections opened by the pool.", nil, nil),
		lifetimeDestroyed: prometheus.NewDesc("pgxpool_max_lifetime_destroys_total",
			"Connections closed for exceeding max lifetime.", nil, nil),
		idleDestroyed: prometheus.NewDesc("pgxpool_max_idle_destroys_total",
			"Connections closed for exceeding max idle time.", nil, nil),
	}
}

// Describe отдаёт описания всех метрик коллектора.
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

// Collect снимает статистику пула и отдаёт её значениями метрик.
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()

	ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue,
		float64(s.AcquiredConns()), "acquired")

	ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue,
		float64(s.IdleConns()), "idle")

	ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue,
		float64(s.ConstructingConns()), "constructing")

	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue,
		float64(s.MaxConns()))

	ch <- prometheus.MustNewConstMetric(c.acquires, prometheus.CounterValue,
		float64(s.AcquireCount()))

	ch <- prometheus.MustNewConstMetric(c.acquireSeconds, prometheus.CounterValue,
		s.AcquireDuration().Seconds())

	ch <- prometheus.MustNewConstMetric(c.emptyAcquires, prometheus.CounterValue,
		float64(s.EmptyAcquireCount()))

	ch <- prometheus.MustNewConstMetric(c.canceledAcquires, prometheus.CounterValue,
		float64(s.CanceledAcquireCount()))

	ch <- prometheus.MustNewConstMetric(c.newConns, prometheus.CounterValue,
		float64(s.NewConnsCount()))

	ch <- prometheus.MustNewConstMetric(c.lifetimeDestroyed, prometheus.CounterValue,
		float64(s.MaxLifetimeDestroyCount()))

	ch <- prometheus.MustNewConstMetric(c.idleDestroyed, prometheus.CounterValue,
		float64(s.MaxIdleDestroyCount()))
}
