package transcodenode

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

// SessionTracking is an exact local monitoring registration. Its methods must
// perform only local bookkeeping; a queued Track must become inert after Remove.
// It carries no execution authority and is never serialized.
type SessionTracking interface {
	Track(context.Context)
	Remove(context.Context)
}

// SessionTrackingPreparer optionally reserves a registration synchronously under
// the producer lifecycle. Network-backed legacy trackers keep their existing
// asynchronous Track path. A rejected nil receipt never falls back to that path.
type SessionTrackingPreparer interface {
	PrepareSessionTracking(context.Context, nodesessions.SessionInfo) SessionTracking
}

func (s *Server) prepareTracking(ctx context.Context, info nodesessions.SessionInfo) SessionTracking {
	if tracker, ok := s.tracker.(SessionTrackingPreparer); ok {
		return tracker.PrepareSessionTracking(ctx, info)
	}
	return nil
}
func (s *Server) trackPrepared(ctx context.Context, info nodesessions.SessionInfo, registration SessionTracking) {
	if _, owned := s.tracker.(SessionTrackingPreparer); owned {
		if registration != nil {
			registration.Track(ctx)
		}
		return
	}
	s.tracker.Track(ctx, info)
}
func (s *Server) removePrepared(ctx context.Context, id string, registration SessionTracking) {
	if _, owned := s.tracker.(SessionTrackingPreparer); owned {
		if registration != nil {
			registration.Remove(ctx)
		}
		return
	}
	s.tracker.Remove(ctx, id)
}
func (s *Server) removeLegacyTracking(ctx context.Context, id string) {
	if _, owned := s.tracker.(SessionTrackingPreparer); !owned {
		s.tracker.Remove(ctx, id)
	}
}
