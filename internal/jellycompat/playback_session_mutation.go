package jellycompat

import (
	"context"
	"sort"
	"sync"
	"time"
)

type playbackSessionMutation struct {
	held chan struct{}
	refs int
}

// lockSessionMutation serializes one row's lifecycle with recipe receipt I/O.
// Callers acquire canonical work first when needed, then session IDs in sorted
// order, and finally the map mutex. No gate wait holds the map mutex.
func (s *PlaybackSessionStore) lockSessionMutation(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.sessionMutationMu.Lock()
	if s.sessionMutations == nil {
		s.sessionMutations = make(map[string]*playbackSessionMutation)
	}
	gate := s.sessionMutations[id]
	if gate == nil {
		gate = &playbackSessionMutation{held: make(chan struct{}, 1)}
		s.sessionMutations[id] = gate
	}
	gate.refs++
	s.sessionMutationMu.Unlock()
	unref := func() {
		s.sessionMutationMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(s.sessionMutations, id)
		}
		s.sessionMutationMu.Unlock()
	}
	select {
	case gate.held <- struct{}{}:
		release := sync.OnceFunc(func() { <-gate.held; unref() })
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		unref()
		return nil, ctx.Err()
	}
}

func (s *PlaybackSessionStore) lockSessionMap(id string) func() {
	// These legacy synchronous mutators have no cancellation parameter. The
	// publication holding their row gate retains its existing bounded I/O budget.
	release, _ := s.lockSessionMutation(context.Background(), id)
	s.mu.Lock()
	return func() { s.mu.Unlock(); release() }
}

// lockSessionSetMap returns with the map locked and all affected rows owned.
// Rechecking the set preserves the original atomic bulk mutation when another
// writer inserts or remaps a row while gates are being acquired. A retry never
// invokes a caller mutation callback and releases every gate before waiting.
func (s *PlaybackSessionStore) lockSessionSetMap(ctx context.Context, fixed []string, match func(string, PlaybackSession) bool) (func(), error) {
	for {
		ids := make(map[string]struct{}, len(fixed))
		for _, id := range fixed {
			ids[id] = struct{}{}
		}
		s.mu.RLock()
		for id, row := range s.sessions {
			if match(id, row) {
				ids[id] = struct{}{}
			}
		}
		s.mu.RUnlock()
		ordered := make([]string, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		releases := make([]func(), 0, len(ordered))
		releaseAll := func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		}
		for _, id := range ordered {
			release, err := s.lockSessionMutation(ctx, id)
			if err != nil {
				releaseAll()
				return nil, err
			}
			releases = append(releases, release)
		}
		s.mu.Lock()
		complete := true
		for id, row := range s.sessions {
			if match(id, row) {
				if _, ok := ids[id]; !ok {
					complete = false
					break
				}
			}
		}
		if complete {
			if err := ctx.Err(); err != nil {
				s.mu.Unlock()
				releaseAll()
				return nil, err
			}
			return func() { s.mu.Unlock(); releaseAll() }, nil
		}
		s.mu.Unlock()
		releaseAll()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

func (s *PlaybackSessionStore) lockExpiredSessionMap(now time.Time) func() {
	release, _ := s.lockSessionSetMap(context.Background(), nil, func(_ string, row PlaybackSession) bool { return !row.ExpiresAt.After(now) })
	return release
}
