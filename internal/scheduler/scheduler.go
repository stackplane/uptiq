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

type ResultHandler interface {
	HandleResult(svc config.Service, res checks.Result)
}

type Scheduler struct {
	log         *slog.Logger
	workerCount int
	jitter      time.Duration

	httpChecker *checks.HTTPChecker
	tcpChecker  *checks.TCPChecker

	metrics *metrics.Metrics
	handler ResultHandler

	jobsCh   chan job
	updateCh chan []config.Service

	wg sync.WaitGroup
}

type job struct {
	svc config.Service
}

type scheduledItem struct {
	svc     config.Service
	nextRun time.Time
	index   int
}

type scheduleHeap []*scheduledItem

func (h scheduleHeap) Len() int           { return len(h) }
func (h scheduleHeap) Less(i, j int) bool { return h[i].nextRun.Before(h[j].nextRun) }
func (h scheduleHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *scheduleHeap) Push(x any)        { *h = append(*h, x.(*scheduledItem)) }
func (h *scheduleHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
func (h scheduleHeap) Peek() *scheduledItem {
	if len(h) == 0 {
		return nil
	}
	return h[0]
}

func New(cfg *config.SitelertConfig, log *slog.Logger, m *metrics.Metrics, handler ResultHandler) (*Scheduler, error) {
	if log == nil {
		log = slog.Default()
	}

	j, err := time.ParseDuration(cfg.Global.Jitter)
	if err != nil {
		return nil, fmt.Errorf("parse global.jitter: %w", err)
	}

	workerCount := cfg.Global.WorkerCount
	if workerCount < 1 {
		workerCount = 1
	}

	return &Scheduler{
		log:         log,
		workerCount: workerCount,
		jitter:      j,
		httpChecker: checks.NewHTTPChecker(),
		tcpChecker:  checks.NewTCPChecker(),
		metrics:     m,
		handler:     handler,
		jobsCh:      make(chan job, workerCount*4),
		updateCh:    make(chan []config.Service, 1),
	}, nil
}

// UpdateServices triggers a schedule rebuild. It does not stop workers.
func (s *Scheduler) UpdateServices(services []config.Service) {
	// Keep only the latest update
	select {
	case s.updateCh <- services:
		return
	default:
		select {
		case <-s.updateCh:
		default:
		}
		s.updateCh <- services
	}
}

func (s *Scheduler) Start(ctx context.Context, services []config.Service) error {
	s.startWorkers(ctx)

	h := &scheduleHeap{}
	heap.Init(h)
	s.rebuildHeap(h, services)

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		next := h.Peek()
		var wait time.Duration
		if next == nil {
			wait = 500 * time.Millisecond
		} else {
			now := time.Now()
			if next.nextRun.After(now) {
				wait = next.nextRun.Sub(now)
			} else {
				wait = 0
			}
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopping", "reason", ctx.Err())
			s.stopWorkers()
			return nil

		case newServices := <-s.updateCh:
			// Rebuild schedule immediately on reload
			s.rebuildHeap(h, newServices)

		case <-timer.C:
			item := h.Peek()
			if item == nil {
				continue
			}
			if item.nextRun.After(time.Now()) {
				continue
			}

			// Due
			item = heap.Pop(h).(*scheduledItem)
			select {
			case <-ctx.Done():
				s.stopWorkers()
				return nil
			case s.jobsCh <- job{svc: item.svc}:
			}

			interval, err := time.ParseDuration(item.svc.Interval)
			if err != nil || interval <= 0 {
				interval = 30 * time.Second
			}
			item.nextRun = time.Now().Add(interval).Add(s.randJitter())
			heap.Push(h, item)
		}
	}
}

func (s *Scheduler) rebuildHeap(h *scheduleHeap, services []config.Service) {
	// Diff logging: added/removed/changed (best-effort)
	prev := make(map[string]config.Service)
	for _, it := range *h {
		prev[it.svc.ID] = it.svc
	}
	next := make(map[string]config.Service)
	for _, svc := range services {
		next[svc.ID] = svc
	}

	var added, removed, changed []string
	for id, svc := range next {
		if old, ok := prev[id]; !ok {
			added = append(added, id)
		} else if !servicesEqualForSchedule(old, svc) {
			changed = append(changed, id)
		}
	}
	for id := range prev {
		if _, ok := next[id]; !ok {
			removed = append(removed, id)
		}
	}

	// Clear heap and rebuild
	*h = (*h)[:0]
	heap.Init(h)

	now := time.Now()
	for _, svc := range services {
		typ := strings.ToLower(strings.TrimSpace(svc.Type))
		if typ != "http" && typ != "tcp" {
			s.log.Warn("skipping unsupported service type",
				"service_id", svc.ID,
				"service_name", svc.Name,
				"type", svc.Type,
			)
			continue
		}

		interval, err := time.ParseDuration(svc.Interval)
		if err != nil || interval <= 0 {
			s.log.Warn("invalid interval; using fallback",
				"service_id", svc.ID,
				"interval", svc.Interval,
			)
		}

		first := now.Add(s.randJitter())
		heap.Push(h, &scheduledItem{svc: svc, nextRun: first})

		s.log.Info("scheduled service",
			"service_id", svc.ID,
			"service_name", svc.Name,
			"type", svc.Type,
			"interval", svc.Interval,
			"timeout", svc.Timeout,
		)
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

func servicesEqualForSchedule(a, b config.Service) bool {
	// Compare only scheduling/check-relevant fields
	return a.ID == b.ID &&
		a.Type == b.Type &&
		a.Interval == b.Interval &&
		a.Timeout == b.Timeout &&
		a.URL == b.URL &&
		a.Method == b.Method &&
		strings.Join(mapToKV(a.Headers), ",") == strings.Join(mapToKV(b.Headers), ",") &&
		strings.Join(intSliceToStr(a.ExpectedStatus), ",") == strings.Join(intSliceToStr(b.ExpectedStatus), ",") &&
		a.Contains == b.Contains &&
		a.Host == b.Host &&
		a.Port == b.Port
}

func mapToKV(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

func intSliceToStr(in []int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, n := range in {
		out = append(out, fmt.Sprintf("%d", n))
	}
	return out
}

func (s *Scheduler) startWorkers(ctx context.Context) {
	for i := 0; i < s.workerCount; i++ {
		s.wg.Add(1)
		go func(workerID int) {
			defer s.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case jb, ok := <-s.jobsCh:
					if !ok {
						return
					}
					s.runJob(ctx, workerID, jb)
				}
			}
		}(i + 1)
	}
}

func (s *Scheduler) stopWorkers() {
	close(s.jobsCh)
	s.wg.Wait()
}

func (s *Scheduler) runJob(parent context.Context, workerID int, jb job) {
	svc := jb.svc

	timeout, err := time.ParseDuration(svc.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := time.Now()

	var res checks.Result
	switch strings.ToLower(strings.TrimSpace(svc.Type)) {
	case "http":
		res = s.httpChecker.Check(ctx, svc)
	case "tcp":
		res = s.tcpChecker.Check(ctx, svc)
	default:
		res = checks.Result{Success: false, Latency: time.Since(start), Error: "unsupported type"}
	}

	if s.metrics != nil {
		s.metrics.Observe(svc, res)
	}
	if s.handler != nil {
		s.handler.HandleResult(svc, res)
	}

	fields := []any{
		"worker", workerID,
		"service_id", svc.ID,
		"service_name", svc.Name,
		"type", svc.Type,
		"success", res.Success,
		"status_code", res.StatusCode,
		"latency_ms", res.Latency.Milliseconds(),
		"timeout", svc.Timeout,
	}

	if strings.EqualFold(svc.Type, "http") {
		fields = append(fields, "target", svc.URL)
	} else if strings.EqualFold(svc.Type, "tcp") {
		fields = append(fields, "target", net.JoinHostPort(svc.Host, fmt.Sprintf("%d", svc.Port)))
	}

	if res.Success {
		s.log.Info("check completed", fields...)
	} else {
		fields = append(fields, "error", res.Error, "elapsed_ms", time.Since(start).Milliseconds())
		s.log.Warn("check failed", fields...)
	}
}

func (s *Scheduler) randJitter() time.Duration {
	if s.jitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(s.jitter)))
}
