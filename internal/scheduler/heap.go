package scheduler

import (
	"container/heap"
	"time"

	"sitelert/internal/config"
)

// scheduledItem represents a service scheduled for checking.
type scheduledItem struct {
	service config.Service
	nextRun time.Time
	index   int
}

// scheduleHeap is a min-heap of scheduled items ordered by nextRun time.
type scheduleHeap []*scheduledItem

// Implement heap.Interface

func (h scheduleHeap) Len() int           { return len(h) }
func (h scheduleHeap) Less(i, j int) bool { return h[i].nextRun.Before(h[j].nextRun) }
func (h scheduleHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *scheduleHeap) Push(x any) {
	item := x.(*scheduledItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *scheduleHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	item.index = -1
	*h = old[:n-1]
	return item
}

// Peek returns the next item without removing it.
func (h scheduleHeap) Peek() *scheduledItem {
	if len(h) == 0 {
		return nil
	}
	return h[0]
}

// newScheduleHeap creates an initialized schedule heap.
func newScheduleHeap() *scheduleHeap {
	h := &scheduleHeap{}
	heap.Init(h)
	return h
}

// clear removes all items from the heap.
func (h *scheduleHeap) clear() {
	*h = (*h)[:0]
	heap.Init(h)
}
