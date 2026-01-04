package scheduler

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sitelert/internal/checks"
	"sitelert/internal/config"
	"sitelert/internal/metrics"
	"strings"
	"sync"
	"time"
)

type Scheduler struct {
	log         *slog.Logger
	workerCount int
	jitter      time.Duration

	httpChecker *checks.HTTPChecker
	metrics     *metrics.Metrics

	jobsCh chan job
	wg     sync.WaitGroup
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
	*h = old[0 : n-1]
	return item
}
func (h scheduleHeap) Peek() *scheduledItem {
	if len(h) == 0 {
		return nil
	}
	return h[0]
}

func NewScheduler(cfg config.SitelertConfig, log *slog.Logger, m *metrics.Metrics) (*Scheduler, error) {
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
		metrics:     m,
		jobsCh:      make(chan job, workerCount*4),
	}, nil
}

func (s *Scheduler) Start(ctx context.Context, services []config.Service) error {
	s.startWorkers(ctx)

	h := &scheduleHeap{}
	heap.Init(h)

	now := time.Now()
	for _, svc := range services {
		typ := strings.ToLower(strings.TrimSpace(svc.Type))
		if typ != "http" {
			s.log.Warn("skipping unsupported service type",
				"service_id", svc.ID,
				"service_name", svc.Name,
				"type", svc.Type,
			)
			continue
		}

		interval, err := time.ParseDuration(svc.Interval)
		if err != nil {
			return fmt.Errorf("parse interval for service %q: %w", svc.ID, err)
		}
		if interval <= 0 {
			return fmt.Errorf("service %q interval must be > 0", svc.ID)
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

	for {
		select {
		case <-ctx.Done():
			s.log.Info("schedule stopping", "reason", ctx.Err())
			s.stopWorkers()
			return nil
		default:
		}

		next := h.Peek()
		if next == nil {
			select {
			case <-ctx.Done():
				s.stopWorkers()
				return nil
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}

		now := time.Now()
		if next.nextRun.After(now) {
			timer := time.NewTimer(next.nextRun.Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				s.stopWorkers()
				return nil
			case <-timer.C:
			}
		}

		item := heap.Pop(h).(*scheduledItem)

		select {
		case <-ctx.Done():
			s.stopWorkers()
			return nil
		case s.jobsCh <- job{svc: item.svc}:
		}

		interval, _ := time.ParseDuration(item.svc.Interval)
		item.nextRun = time.Now().Add(interval).Add(s.randJitter())
		heap.Push(h, item)
	}
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
	res := s.httpChecker.Check(ctx, svc)

	if s.metrics != nil {
		s.metrics.Observe(svc, res)
	}

	fields := []any{
		"worker", workerID,
		"service_id", svc.ID,
		"service_name", svc.Name,
		"type", svc.Type,
		"url", svc.URL,
		"success", res.Success,
		"status_code", res.StatusCode,
		"latency_ms", res.Latency.Milliseconds(),
		"timeout", svc.Timeout,
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
