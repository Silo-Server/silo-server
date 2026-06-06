package adminjob

import (
	"context"
	"sync"
)

// CancelRegistry bridges HTTP cancellation requests to in-process job contexts.
// Queued jobs are cancelled in the repository; running jobs need their active
// context cancelled so executors stop doing work before the row is finalized.
type CancelRegistry struct {
	mu      sync.Mutex
	cancels map[string]cancelEntry
}

type cancelEntry struct {
	cancel context.CancelFunc
	token  *struct{}
}

func NewCancelRegistry() *CancelRegistry {
	return &CancelRegistry{cancels: make(map[string]cancelEntry)}
}

func (r *CancelRegistry) Register(jobID string, cancel context.CancelFunc) func() {
	if r == nil || jobID == "" || cancel == nil {
		return func() {}
	}
	token := &struct{}{}
	r.mu.Lock()
	r.cancels[jobID] = cancelEntry{cancel: cancel, token: token}
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		if entry := r.cancels[jobID]; entry.token == token {
			delete(r.cancels, jobID)
		}
		r.mu.Unlock()
	}
}

func (r *CancelRegistry) Cancel(jobID string) bool {
	if r == nil || jobID == "" {
		return false
	}
	r.mu.Lock()
	cancel := r.cancels[jobID].cancel
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
