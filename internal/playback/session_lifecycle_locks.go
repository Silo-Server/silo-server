package playback

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// sessionLifecycleLock serializes stop/expiry and reconstruction for one
// public session ID while allowing unrelated sessions to proceed concurrently.
// References include both holders and waiters, so entries are removed as soon
// as the last operation exits and the lock map cannot grow with old IDs.
type sessionLifecycleLock struct {
	mu   sync.Mutex
	refs int
}

func (m *SessionManager) acquireSessionLifecycleLock(sessionID string) func() {
	m.sessionLifecycleMu.Lock()
	if m.sessionLifecycleLocks == nil {
		m.sessionLifecycleLocks = make(map[string]*sessionLifecycleLock)
	}
	entry := m.sessionLifecycleLocks[sessionID]
	if entry == nil {
		entry = &sessionLifecycleLock{}
		m.sessionLifecycleLocks[sessionID] = entry
	}
	entry.refs++
	m.sessionLifecycleMu.Unlock()

	if hook := m.sessionLifecycleWaitHook; hook != nil {
		hook(sessionID)
	}
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.sessionLifecycleMu.Lock()
		entry.refs--
		if entry.refs == 0 && m.sessionLifecycleLocks[sessionID] == entry {
			delete(m.sessionLifecycleLocks, sessionID)
		}
		m.sessionLifecycleMu.Unlock()
	}
}

// WithSessionGeneration serializes a multi-step operation against replacement
// or reconstruction of the same reusable public session ID. The global session
// mutex is held only for the exact identity check; operation may perform process
// or network I/O while the keyed lifecycle guard remains held.
func (m *SessionManager) WithSessionGeneration(
	ctx context.Context,
	sessionID string,
	generation string,
	operation func() error,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("%w: session ID is required", ErrSessionGenerationTombstoneUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release := m.acquireSessionLifecycleLock(sessionID)
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.RLock()
	current := m.sessions[sessionID]
	if current == nil {
		m.mu.RUnlock()
		return ErrSessionNotFound
	}
	if current.Generation != generation {
		m.mu.RUnlock()
		return ErrSessionSuperseded
	}
	m.mu.RUnlock()

	if operation == nil {
		return nil
	}
	return operation()
}
