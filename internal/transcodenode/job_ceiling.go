package transcodenode

import (
	"context"
	"errors"
	"sync"

	"github.com/Silo-Server/silo-server/internal/executionlease"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const errJobCeiling = "node is at its operator job limit; retry shortly"

type jobIdentityKey struct{}
type jobKey struct {
	workID    string
	anonymous uint64
}
type jobCeiling struct {
	mu       sync.Mutex
	limit    int
	sequence uint64
	refs     map[jobKey]int
	sessions map[*playback.TranscodeSession]sessionJobLease
}

type sessionJobLease struct {
	tracking SessionTracking
	context  context.Context //nolint:containedctx // Session lease retains execution ownership until releaseSessionJob removes it.
	release  func()
}

// SetJobCeiling sets the local operator's concurrent work ceiling. Zero keeps
// the default unlimited behavior. Lowering a ceiling preserves existing work
// and refuses new identities until capacity is available.
func (s *Server) SetJobCeiling(limit int) error {
	if limit < 0 {
		return errors.New("job ceiling must not be negative")
	}
	s.jobs.mu.Lock()
	s.jobs.limit = limit
	s.jobs.mu.Unlock()
	return nil
}

// WithJobIdentity carries the authenticated core assignment's work identity
// through execution and replacement. It is never populated from a client header.
func WithJobIdentity(ctx context.Context, workID string) context.Context {
	if workID == "" {
		return ctx
	}
	return context.WithValue(ctx, jobIdentityKey{}, workID)
}

func (s *Server) acquireJob(ctx context.Context, fallbackWorkID string) (func(), bool) {
	_, release, ok := s.acquireJobContext(ctx, fallbackWorkID)
	return release, ok
}

func (s *Server) acquireJobContext(ctx context.Context, fallbackWorkID string) (context.Context, func(), bool) {
	if ctx.Err() != nil {
		return ctx, nil, false
	}
	work, _ := ctx.Value(jobIdentityKey{}).(string)
	if work == "" {
		work = fallbackWorkID
	}
	c := &s.jobs
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refs == nil {
		c.refs = map[jobKey]int{}
	}
	key := jobKey{workID: work}
	if work == "" {
		c.sequence++
		key.anonymous = c.sequence
	}
	if c.refs[key] == 0 && c.limit > 0 && len(c.refs) >= c.limit {
		return ctx, nil, false
	}
	c.refs[key]++
	release := c.release(key)
	jobCtx := executionlease.WithRetainer(ctx, func() (func(), error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.refs[key] == 0 {
			return nil, errors.New("execution work is no longer held")
		}
		c.refs[key]++
		return c.release(key), nil
	})
	return jobCtx, release, true
}

func (c *jobCeiling) release(key jobKey) func() {
	return sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.refs[key] > 1 {
			c.refs[key]--
		} else {
			delete(c.refs, key)
		}
	})
}

func (s *Server) holdSessionJob(session *playback.TranscodeSession, ctx context.Context, release func(), tracking SessionTracking) {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	if s.jobs.sessions == nil {
		s.jobs.sessions = map[*playback.TranscodeSession]sessionJobLease{}
	}
	s.jobs.sessions[session] = sessionJobLease{context: ctx, release: release, tracking: tracking}
}

func (s *Server) sessionJobContext(ctx context.Context, session *playback.TranscodeSession) context.Context {
	s.jobs.mu.Lock()
	lease, ok := s.jobs.sessions[session]
	s.jobs.mu.Unlock()
	if !ok {
		return ctx
	}
	return executionlease.WithRetainer(ctx, func() (func(), error) { return executionlease.Retain(lease.context) })
}

func (s *Server) releaseSessionJob(session *playback.TranscodeSession) {
	s.jobs.mu.Lock()
	lease := s.jobs.sessions[session]
	delete(s.jobs.sessions, session)
	s.jobs.mu.Unlock()
	if lease.tracking != nil {
		lease.tracking.Remove(context.Background())
	}
	if lease.release != nil {
		lease.release()
	}
}
