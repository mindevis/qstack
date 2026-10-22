package db

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolCollector exports pgxpool statistics as qs_db_pool_* metrics (REQ-OBS-001).
type PoolCollector struct {
	pool *pgxpool.Pool

	acquired, idle, total, max *prometheus.Desc
	acquires, emptyAcquires    *prometheus.Desc
	acquireSeconds             *prometheus.Desc
}

// NewPoolCollector returns a collector for pool; register it with
// prometheus.Registerer.MustRegister.
func NewPoolCollector(pool *pgxpool.Pool) *PoolCollector {
	d := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("qs_db_pool_"+name, help, nil, nil)
	}
	return &PoolCollector{
		pool:           pool,
		acquired:       d("acquired_conns", "Connections currently in use."),
		idle:           d("idle_conns", "Idle connections."),
		total:          d("total_conns", "Open connections."),
		max:            d("max_conns", "Maximum pool size."),
		acquires:       d("acquires_total", "Successful connection acquisitions."),
		emptyAcquires:  d("empty_acquires_total", "Acquisitions that had to wait for a connection."),
		acquireSeconds: d("acquire_seconds_total", "Total time spent waiting to acquire connections."),
	}
}

// Describe implements prometheus.Collector.
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.acquired, c.idle, c.total, c.max, c.acquires, c.emptyAcquires, c.acquireSeconds} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.max, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.acquires, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquires, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireSeconds, prometheus.CounterValue, s.AcquireDuration().Seconds())
}
