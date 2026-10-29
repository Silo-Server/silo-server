package keptsub

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errStillReading means a caller stopped waiting for a read that goes on.
var errStillReading = errors.New("keptsub: the track is still being read")

// flightKey names one read of a track: of one source version, in the
// foreground or the background. Foreground and background reads don't
// share a flight, since they read differently; the claim keeps them from
// reading at once.
type flightKey struct {
	fileID     int
	stream     int
	version    sourceVersion
	background bool
}

// flight is a read shared by every caller that asks for it while it runs.
type flight struct {
	done    chan struct{}
	track   *Track
	err     error
	waiters int
	cancel  context.CancelFunc
}

// shared runs fn once for every concurrent caller with the same key. fn
// runs on its own context, which ends when every caller whose context
// ended has gone, so a caller giving up doesn't fail the others, and the
// last one stops the read. A caller that waits longer than wait, when wait
// is set, gets errStillReading and leaves the read running: what it reads
// is still kept.
func (s *Store) shared(ctx context.Context, key flightKey, wait time.Duration, fn func(context.Context) (*Track, error)) (*Track, error) {
	s.mu.Lock()
	f := s.flights[key]
	if f == nil {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimLease)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		s.flights[key] = f
		go s.run(readCtx, key, f, fn)
	}
	f.waiters++
	s.mu.Unlock()
	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-f.done:
		return f.track, f.err
	case <-timeout:
		return nil, errStillReading
	case <-ctx.Done():
		s.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
		}
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *Store) run(ctx context.Context, key flightKey, f *flight, fn func(context.Context) (*Track, error)) {
	defer func() {
		if r := recover(); r != nil {
			f.track, f.err = nil, fmt.Errorf("keptsub: track read panicked: %v", r)
		}
		f.cancel()
		s.mu.Lock()
		delete(s.flights, key)
		s.mu.Unlock()
		close(f.done)
	}()
	f.track, f.err = fn(ctx)
}
