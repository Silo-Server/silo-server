package keptsub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// errStillReading means a caller stopped waiting for a read that goes on.
var errStillReading = errors.New("keptsub: the track is still being read")

// maxWholeFlights bounds reads running, queued, or finishing after cancellation.
// Admitted foreground reads may continue after their callers leave.
// maxFileFlights bounds them per source file, so one file's tracks can't
// take every place.
const (
	maxWholeFlights = 16
	maxFileFlights  = 8
)

// errReadLimit refuses a read past maxWholeFlights or maxFileFlights; a
// request answers it busy, with a Retry-After.
var errReadLimit = fmt.Errorf("keptsub: too many whole-track reads: %w", playback.ErrSubtitleReadsBusy)

// flightKey names one read of a track: of one source version.
type flightKey struct {
	fileID  int
	stream  int
	version sourceVersion
}

// flight is a read shared by every caller that asks for it while it runs.
// A background caller (a prefetch) starts it at background priority; once a
// foreground caller (a request) joins, it is a foreground read: its I/O
// priority is raised, and once it has started, holding a read slot, it no
// longer stops when its callers go.
type flight struct {
	done  chan struct{}
	track *Track
	err   error
	// prio is a background read's priority, raised when a request joins.
	prio *embeddedsub.Priority
	// waiters, foreground, started and cancel are guarded by Store.mu.
	waiters    int
	foreground bool
	started    bool
	cancel     context.CancelFunc
}

// isForeground reports whether a request has joined the read.
func (s *Store) isForeground(f *flight) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f.foreground
}

// shared runs fn once for every concurrent caller with the same key. fn
// runs on its own context, without a deadline: fn bounds its own steps.
//
// A foreground read that has started finishes and keeps the track whether
// or not anyone still waits: a client that gave up retries, and finds the
// track kept or still being read. A background read, or one still waiting
// for a slot, stops once every caller has gone. A caller that waits longer
// than wait, when wait is set, gets errStillReading and goes.
func (s *Store) shared(ctx context.Context, key flightKey, background bool, wait time.Duration, fn func(context.Context, *flight) (*Track, error)) (*Track, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	f := s.flights[key]
	switch {
	case f == nil:
		if s.activeFlights >= maxWholeFlights || s.fileFlights[key.fileID] >= s.fileFlightLimit {
			s.mu.Unlock()
			return nil, errReadLimit
		}
		s.activeFlights++
		if s.fileFlights == nil {
			s.fileFlights = map[int]int{}
		}
		s.fileFlights[key.fileID]++
		readCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight{done: make(chan struct{}), cancel: cancel, foreground: !background}
		if background {
			f.prio = embeddedsub.NewBackgroundPriority()
		}
		s.flights[key] = f
		go s.run(readCtx, key, f, fn)
	case !background && !f.foreground:
		f.foreground = true
		f.prio.Raise()
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
		s.leave(key, f)
		return nil, errStillReading
	case <-ctx.Done():
		s.leave(key, f)
		return nil, ctx.Err()
	}
}

// leave records that a caller stopped waiting for f. When none waits, a
// read that is background or hasn't started stops.
func (s *Store) leave(key flightKey, f *flight) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f.waiters--
	if f.waiters > 0 || (f.foreground && f.started) {
		return
	}
	f.cancel()
	// A caller arriving now starts afresh instead of joining a read that is
	// stopping.
	if s.flights[key] == f {
		delete(s.flights, key)
	}
}

// start marks f as started once it holds a read slot, unless its callers
// went while it waited.
func (s *Store) start(ctx context.Context, f *flight) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	f.started = true
	return nil
}

func (s *Store) run(ctx context.Context, key flightKey, f *flight, fn func(context.Context, *flight) (*Track, error)) {
	defer func() {
		if r := recover(); r != nil {
			f.track, f.err = nil, fmt.Errorf("keptsub: track read panicked: %v", r)
		}
		f.cancel()
		s.mu.Lock()
		s.activeFlights--
		if s.fileFlights[key.fileID]--; s.fileFlights[key.fileID] <= 0 {
			delete(s.fileFlights, key.fileID)
		}
		if s.flights[key] == f {
			delete(s.flights, key)
		}
		s.mu.Unlock()
		close(f.done)
	}()
	f.track, f.err = fn(ctx, f)
}
