package metadata

import (
	"container/heap"
	"sync"
)

// RefreshEntry is an item in the refresh queue.
type RefreshEntry struct {
	ContentID string
	Priority  RefreshPriority
	Mode      RefreshMode
	index     int // heap index
}

// RefreshQueue is a thread-safe priority queue with deduplication.
type RefreshQueue struct {
	mu      sync.Mutex
	entries refHeap
	byID    map[string]*RefreshEntry
}

// NewRefreshQueue creates an empty refresh queue.
func NewRefreshQueue() *RefreshQueue {
	q := &RefreshQueue{
		byID: make(map[string]*RefreshEntry),
	}
	heap.Init(&q.entries)
	return q
}

// Enqueue adds an item to the queue. If already queued, keeps higher priority.
func (q *RefreshQueue) Enqueue(contentID string, priority RefreshPriority, mode RefreshMode) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if existing, ok := q.byID[contentID]; ok {
		if priority < existing.Priority {
			existing.Priority = priority
			existing.Mode = mode
			heap.Fix(&q.entries, existing.index)
		}
		return
	}

	entry := &RefreshEntry{
		ContentID: contentID,
		Priority:  priority,
		Mode:      mode,
	}
	heap.Push(&q.entries, entry)
	q.byID[contentID] = entry
}

// Dequeue removes and returns the highest priority entry.
func (q *RefreshQueue) Dequeue() (RefreshEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.entries.Len() == 0 {
		return RefreshEntry{}, false
	}

	entry := heap.Pop(&q.entries).(*RefreshEntry) //nolint:errcheck,forcetypeassert // refHeap holds only *RefreshEntry
	delete(q.byID, entry.ContentID)
	return *entry, true
}

// Len returns the number of items in the queue.
func (q *RefreshQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.entries.Len()
}

// refHeap implements heap.Interface for RefreshEntry.
type refHeap []*RefreshEntry

func (h refHeap) Len() int           { return len(h) }
func (h refHeap) Less(i, j int) bool { return h[i].Priority < h[j].Priority }
func (h refHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *refHeap) Push(x any) {
	entry := x.(*RefreshEntry) //nolint:errcheck,forcetypeassert // Enqueue is the only caller of heap.Push and passes a *RefreshEntry
	entry.index = len(*h)
	*h = append(*h, entry)
}
func (h *refHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[:n-1]
	return entry
}
