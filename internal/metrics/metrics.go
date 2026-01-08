// Package metrics provides Prometheus metrics for monitoring.
package metrics

import (
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"sitelert/internal/checks"
	"sitelert/internal/config"
)

// Labels used in metrics.
const (
	LabelServiceID   = "service_id"
	LabelServiceName = "service_name"
	LabelType        = "type"
	LabelResult      = "result"
)

// Result label values.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

// Collector contains all sitelert metrics.
type Collector struct {
	CheckTotal           *prometheus.CounterVec
	CheckLatencySeconds  *prometheus.HistogramVec
	Up                   *prometheus.GaugeVec
	LastSuccessTimestamp *prometheus.GaugeVec
	BuildInfo            *prometheus.GaugeVec
	ConfigReloadSuccess  prometheus.Gauge

	mu          sync.Mutex
	initialized map[string]struct{}
}

// Bundle combines a registry with its collector.
type Bundle struct {
	Registry  *prometheus.Registry
	Collector *Collector
}

// NewBundle creates a new metrics bundle with all collectors registered.
func NewBundle() *Bundle {
	reg := prometheus.NewRegistry()
	col := newCollector()

	reg.MustRegister(
		col.CheckTotal,
		col.CheckLatencySeconds,
		col.Up,
		col.LastSuccessTimestamp,
		col.BuildInfo,
		col.ConfigReloadSuccess,
	)

	// Set build info immediately
	col.BuildInfo.WithLabelValues(runtime.Version(), runtime.GOOS, runtime.GOARCH).Set(1)
	col.ConfigReloadSuccess.Set(1)

	return &Bundle{Registry: reg, Collector: col}
}

func newCollector() *Collector {
	serviceLabels := []string{LabelServiceID, LabelServiceName, LabelType}

	return &Collector{
		CheckTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sitelert_check_total",
				Help: "Total number of checks executed, labeled by result.",
			},
			append(serviceLabels, LabelResult),
		),

		CheckLatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "sitelert_check_latency_seconds",
				Help:    "Check latency in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			serviceLabels,
		),

		Up: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sitelert_up",
				Help: "Whether the last check succeeded (1) or failed (0).",
			},
			serviceLabels,
		),

		LastSuccessTimestamp: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sitelert_last_success_timestamp",
				Help: "Unix timestamp of the last successful check.",
			},
			serviceLabels,
		),

		BuildInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sitelert_build_info",
				Help: "Build/runtime info exposed as a gauge set to 1.",
			},
			[]string{"go_version", "os", "arch"},
		),

		ConfigReloadSuccess: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "sitelert_config_reload_success",
				Help: "Whether the last config reload succeeded (1) or failed (0).",
			},
		),

		initialized: make(map[string]struct{}),
	}
}

// EnsureServices pre-creates metric series for services without resetting existing ones.
func (c *Collector) EnsureServices(services []config.Service) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, svc := range services {
		key := svc.ID + "|" + svc.Name + "|" + svc.Type
		if _, exists := c.initialized[key]; exists {
			continue
		}
		c.initialized[key] = struct{}{}

		labels := []string{svc.ID, svc.Name, svc.Type}

		// Initialize gauges
		c.Up.WithLabelValues(labels...).Set(0)
		c.LastSuccessTimestamp.WithLabelValues(labels...).Set(0)

		// Touch counters (initialize at 0)
		c.CheckTotal.WithLabelValues(svc.ID, svc.Name, svc.Type, ResultSuccess).Add(0)
		c.CheckTotal.WithLabelValues(svc.ID, svc.Name, svc.Type, ResultFailure).Add(0)

		// Touch histogram
		_, _ = c.CheckLatencySeconds.GetMetricWithLabelValues(labels...)
	}
}

// Observe records a check result in metrics.
func (c *Collector) Observe(svc config.Service, res checks.Result) {
	labels := []string{svc.ID, svc.Name, svc.Type}

	c.CheckLatencySeconds.WithLabelValues(labels...).Observe(res.Latency.Seconds())

	if res.Success {
		c.CheckTotal.WithLabelValues(svc.ID, svc.Name, svc.Type, ResultSuccess).Inc()
		c.Up.WithLabelValues(labels...).Set(1)
		c.LastSuccessTimestamp.WithLabelValues(labels...).Set(float64(time.Now().Unix()))
	} else {
		c.CheckTotal.WithLabelValues(svc.ID, svc.Name, svc.Type, ResultFailure).Inc()
		c.Up.WithLabelValues(labels...).Set(0)
	}
}
