package presence

import (
	"context"
	"sync"
)

// Registry tracks whether a user has at least one live realtime connection.
// Used to suppress push notifications to users who are actively connected.
type Registry interface {
	// Add registers one live connection for userID and returns a release
	// function that must be called on disconnect. The returned function is
	// safe to call multiple times.
	Add(ctx context.Context, userID int) (release func())
	// Connected reports whether userID has any live connection.
	Connected(ctx context.Context, userID int) bool
}

// MemoryRegistry is a process-local refcount registry.
type MemoryRegistry struct {
	mu     sync.Mutex
	counts map[int]int
}

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{counts: make(map[int]int)}
}

func (m *MemoryRegistry) Add(_ context.Context, userID int) func() {
	m.mu.Lock()
	m.counts[userID]++
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.counts[userID] > 0 {
				m.counts[userID]--
				if m.counts[userID] == 0 {
					delete(m.counts, userID)
				}
			}
			m.mu.Unlock()
		})
	}
}

func (m *MemoryRegistry) Connected(_ context.Context, userID int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[userID] > 0
}

var _ Registry = (*MemoryRegistry)(nil)
