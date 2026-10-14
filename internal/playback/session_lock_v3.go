package playback

import (
	"context"
	"sync"
)

// SessionLocksV3 bounds lock bookkeeping to current holders and waiters and
// allows canceled requests to leave the queue without waiting for a holder.
type SessionLocksV3 struct {
	mu      sync.Mutex
	entries map[string]*sessionLockV3
}

type sessionLockV3 struct {
	ready chan struct{}
	refs  int
}

func (s *SessionLocksV3) Acquire(ctx context.Context, sessionID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.entries == nil {
		s.entries = make(map[string]*sessionLockV3)
	}
	entry := s.entries[sessionID]
	if entry == nil {
		entry = &sessionLockV3{ready: make(chan struct{}, 1)}
		entry.ready <- struct{}{}
		s.entries[sessionID] = entry
	}
	entry.refs++
	s.mu.Unlock()
	forget := func() {
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.entries, sessionID)
		}
		s.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case <-entry.ready:
	}
	var once sync.Once
	release := func() { once.Do(func() { entry.ready <- struct{}{}; forget() }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
