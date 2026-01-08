// Package scheduler manages check scheduling and execution.
package scheduler

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"sitelert/internal/checks"
	"sitelert/internal/config"
	"sitelert/internal/metrics"
)

// Default configuration values.
const (
	defaultInterval     = 30 * time.Second
	defaultTimeout      = 5 * time.Second
	defaultWaitInterval = 500 * time.Millisecond
	minWorkerCount      = 1
)

// ResultHandler processes check results.
type ResultHandler interface {
	HandleResult(svc config.Service, res checks.Result)
}

// Scheduler manages periodic health checks.
type Scheduler struct {
	log         *slog.Logger
	workerCount int
	jitter      time.Duration

	checkers *checks.Factory
	metrics  *metrics.Collector
	handler  ResultHandler

	jobsCh   chan config.Service
	updateCh chan []config.Service

	wg sync.WaitGroup
}

// New creates a new scheduler.
func New(cfg *config.Config, log *slog.Logger, m *metrics.Collector, handler ResultHandler) (*Scheduler, error) {
	if log == nil {
		log = slog.Default()
	}

	jitter, err := time.ParseDuration(cfg.Global.Jitter)
	if err != nil {
		return nil, fmt.Errorf("parse global.jitter: %w", err)
	}

	workerCount := cfg.Global.WorkerCount
	if workerCount < minWorkerCount {
		workerCount = minWorkerCount
	}

	return &Scheduler{
		log:         log,
		workerCount: workerCount,
		jitter:      jitter,
		checkers:    checks.NewFactory(),
		metrics:     m,
		handler:     handler,
		jobsCh:      make(chan config.Service, workerCount*4),
		updateCh:    make(chan []config.Service, 1),
	}, nil
}

// UpdateServices triggers a schedule rebuild with new services.
func (s *Scheduler) UpdateServices(services []config.Service) {
	// Keep only the latest update (drop older pending updates)
	select {
	case s.updateCh <- services:
	default:
		select {
		case <-s.updateCh:
		default:
		}
		s.updateCh <- services
	}
}

// Start begins the scheduling loop.
func (s *Scheduler) Start(ctx context.Context, services []config.Service) error {
	s.startWorkers(ctx)

	h := newScheduleHeap()
	s.rebuildHeap(h, services)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		wait := s.nextWaitDuration(h)
		resetTimer(timer, wait)

		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopping", "reason", ctx.Err())
			s.stopWorkers()
			return nil

		case newServices := <-s.updateCh:
			s.rebuildHeap(h, newServices)

		case <-timer.C:
			s.processReadyItems(ctx, h)
		}
	}
}

func (s *Scheduler) nextWaitDuration(h *scheduleHeap) time.Duration {
	next := h.Peek()
	if next == nil {
		return defaultWaitInterval
	}

	wait := time.Until(next.nextRun)
	if wait < 0 {
		return 0
	}
	return wait
}

func (s *Scheduler) processReadyItems(ctx context.Context, h *scheduleHeap) {
	for {
		item := h.Peek()
		if item == nil || item.nextRun.After(time.Now()) {
			return
		}

		item = heap.Pop(h).(*scheduledItem)

		select {
		case <-ctx.Done():
			return
		case s.jobsCh <- item.service:
		}

		// Reschedule
		interval := parseIntervalOrDefault(item.service.Interval)
		item.nextRun = time.Now().Add(interval).Add(s.randomJitter())
		heap.Push(h, item)
	}
}

func (s *Scheduler) rebuildHeap(h *scheduleHeap, services []config.Service) {
	prev := s.collectServiceIDs(h)

	h.clear()

	now := time.Now()
	current := make(map[string]config.Service)

	for _, svc := range services {
		if !s.isValidServiceType(svc) {
			continue
		}

		current[svc.ID] = svc
		first := now.Add(s.randomJitter())
		heap.Push(h, &scheduledItem{service: svc, nextRun: first})

		s.log.Info("scheduled service",
			"service_id", svc.ID,
			"service_name", svc.Name,
			"type", svc.Type,
			"interval", svc.Interval,
			"timeout", svc.Timeout,
		)
	}

	s.logScheduleChanges(prev, current)
}

func (s *Scheduler) collectServiceIDs(h *scheduleHeap) map[string]config.Service {
	result := make(map[string]config.Service)
	for _, item := range *h {
		result[item.service.ID] = item.service
	}
	return result
}

func (s *Scheduler) isValidServiceType(svc config.Service) bool {
	typ := strings.ToLower(strings.TrimSpace(svc.Type))
	if typ != "http" && typ != "tcp" {
		s.log.Warn("skipping unsupported service type",
			"service_id", svc.ID,
			"service_name", svc.Name,
			"type", svc.Type,
		)
		return false
	}
	return true
}

func (s *Scheduler) logScheduleChanges(prev, current map[string]config.Service) {
	var added, removed, changed []string

	for id, svc := range current {
		if old, ok := prev[id]; !ok {
			added = append(added, id)
		} else if !servicesEqual(old, svc) {
			changed = append(changed, id)
		}
	}

	for id := range prev {
		if _, ok := current[id]; !ok {
			removed = append(removed, id)
		}
	}

	if len(added)+len(removed)+len(changed) > 0 {
		s.log.Info("schedule reloaded",
			"added", added,
			"removed", removed,
			"changed", changed,
		)
	} else {
		s.log.Info("schedule reloaded (no changes)")
	}
}

func (s *Scheduler) startWorkers(ctx context.Context) {
	for i := 0; i < s.workerCount; i++ {
		s.wg.Add(1)
		go s.worker(ctx, i+1)
	}
}

func (s *Scheduler) worker(ctx context.Context, id int) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case svc, ok := <-s.jobsCh:
			if !ok {
				return
			}
			s.runCheck(ctx, id, svc)
		}
	}
}

func (s *Scheduler) stopWorkers() {
	close(s.jobsCh)
	s.wg.Wait()
}

func (s *Scheduler) runCheck(parent context.Context, workerID int, svc config.Service) {
	timeout := parseTimeoutOrDefault(svc.Timeout)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := time.Now()
	res := s.checkers.Check(ctx, svc)

	if s.metrics != nil {
		s.metrics.Observe(svc, res)
	}
	if s.handler != nil {
		s.handler.HandleResult(svc, res)
	}

	s.logCheckResult(workerID, svc, res, start)
}

func (s *Scheduler) logCheckResult(workerID int, svc config.Service, res checks.Result, start time.Time) {
	fields := []any{
		"worker", workerID,
		"service_id", svc.ID,
		"service_name", svc.Name,
		"type", svc.Type,
		"success", res.Success,
		"status_code", res.StatusCode,
		"latency_ms", res.Latency.Milliseconds(),
		"timeout", svc.Timeout,
		"target", targetForService(svc),
	}

	if res.Success {
		s.log.Info("check completed", fields...)
	} else {
		fields = append(fields,
			"error", res.Error,
			"elapsed_ms", time.Since(start).Milliseconds(),
		)
		s.log.Warn("check failed", fields...)
	}
}

func (s *Scheduler) randomJitter() time.Duration {
	if s.jitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(s.jitter)))
}

// Helper functions

func targetForService(svc config.Service) string {
	if svc.IsHTTP() {
		return svc.URL
	}
	if svc.IsTCP() {
		return net.JoinHostPort(svc.Host, fmt.Sprintf("%d", svc.Port))
	}
	return ""
}

func parseIntervalOrDefault(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return defaultInterval
}

func parseTimeoutOrDefault(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return defaultTimeout
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func servicesEqual(a, b config.Service) bool {
	return a.ID == b.ID &&
		a.Type == b.Type &&
		a.Interval == b.Interval &&
		a.Timeout == b.Timeout &&
		a.URL == b.URL &&
		a.Method == b.Method &&
		mapsEqual(a.Headers, b.Headers) &&
		slices.Equal(a.ExpectedStatus, b.ExpectedStatus) &&
		a.Contains == b.Contains &&
		a.Host == b.Host &&
		a.Port == b.Port
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
