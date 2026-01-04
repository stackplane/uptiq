package metrics

import (
	"fmt"
	"runtime"
	"sitelert/internal/checks"
	"sitelert/internal/config"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	CheckTotal           *prometheus.CounterVec
	CheckLatencySeconds  *prometheus.HistogramVec
	Up                   *prometheus.GaugeVec
	LastSuccessTimestamp *prometheus.GaugeVec
	BuildInfo            *prometheus.GaugeVec
}

type Bundle struct {
	Registry *prometheus.Registry
	Metrics  *Metrics
}

func NewBundle() *Bundle {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		CheckTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sitelert_check_total",
				Help: "Total number of checks executed, labeled by result.",
			},
			[]string{"service_id", "service_name", "type", "result"},
		),

		CheckLatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "sitelert_check_latency_seconds",
				Help:    "Check latency in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"service_id", "service_name", "type"},
		),

		Up: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sitelert_up",
				Help: "Whether the last check succeeded (1) or failed (0).",
			},
			[]string{"service_id", "service_name", "type"},
		),

		LastSuccessTimestamp: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sitelert_last_success_timestamp",
				Help: "Unix timestamp of the last successful check.",
			},
			[]string{"service_id", "service_name", "type"},
		),

		BuildInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sitelert_build_info",
				Help: "Build/runtime info exposed as a gauge set to 1.",
			},
			[]string{"go_version", "os", "arch"},
		),
	}

	// Register all collectors to this registry
	reg.MustRegister(
		m.CheckTotal,
		m.CheckLatencySeconds,
		m.Up,
		m.LastSuccessTimestamp,
		m.BuildInfo,
	)

	// Set build info series (ensures /metrics isn't empty even before checks run)
	m.BuildInfo.WithLabelValues(runtime.Version(), runtime.GOOS, runtime.GOARCH).Set(1)

	return &Bundle{Registry: reg, Metrics: m}
}

// InitServices pre-creates time series so /metrics is immediately useful.
// This also ensures "sitelert_up" exists for each service even before first run.
func (m *Metrics) InitServices(services []config.Service) {
	for _, s := range services {
		typ := s.Type
		labels := []string{s.ID, s.Name, typ}

		// Create series with initial values
		m.Up.WithLabelValues(labels...).Set(0)
		m.LastSuccessTimestamp.WithLabelValues(labels...).Set(0)

		// Create both counter series (success/failure) at 0
		m.CheckTotal.WithLabelValues(s.ID, s.Name, typ, "success").Add(0)
		m.CheckTotal.WithLabelValues(s.ID, s.Name, typ, "failure").Add(0)

		// Create histogram series
		m.CheckLatencySeconds.WithLabelValues(labels...).Observe(0)
	}
}

// Observe updates metrics for a check result.
func (m *Metrics) Observe(svc config.Service, res checks.Result) {
	typ := svc.Type
	id := svc.ID
	name := svc.Name

	// latency histogram
	m.CheckLatencySeconds.WithLabelValues(id, name, typ).Observe(res.Latency.Seconds())

	if res.Success {
		m.CheckTotal.WithLabelValues(id, name, typ, "success").Inc()
		m.Up.WithLabelValues(id, name, typ).Set(1)
		m.LastSuccessTimestamp.WithLabelValues(id, name, typ).Set(float64(time.Now().Unix()))
		return
	}

	m.CheckTotal.WithLabelValues(id, name, typ, "failure").Inc()
	m.Up.WithLabelValues(id, name, typ).Set(0)
}

// Optional helper for debugging / future use
func (m *Metrics) String() string {
	return fmt.Sprintf("Metrics(check_total=%p)", m.CheckTotal)
}
