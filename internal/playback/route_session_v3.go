package playback

import (
	"context"
	"sync"
	"time"
)

// RouteSessionStoreV3 serializes executor moves with replans across every
// handler sharing the store. Implementations keep one owned database backend
// through route confirmation; a failed write is never proof of rejection.
type RouteSessionStoreV3 interface {
	AcquireRouteSession(context.Context, string) (RouteSessionV3, error)
}

type RouteSessionV3 interface {
	// GetAttempt includes expired and stopped records, distinguishing them from
	// genuinely absent legacy attempts. After an uncertain write it first joins
	// the old backend by reacquiring the same serialization boundary.
	GetAttempt(context.Context) (*AttemptRecordV3, error)
	// CompareAndSwap changes only the frozen route while every expected attempt
	// identity and old route still matches an unexpired, unstopped record.
	CompareAndSwap(context.Context, AttemptRecordV3, TranscodeRoute) (bool, error)
	Close()
}

type memoryRouteSessionV3 struct {
	store     *MemoryPlanStoreV3
	sessionID string
	release   func()
	once      sync.Once
}

func (s *MemoryPlanStoreV3) AcquireRouteSession(ctx context.Context, sessionID string) (RouteSessionV3, error) {
	release, err := s.AcquireSessionLock(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &memoryRouteSessionV3{store: s, sessionID: sessionID, release: release}, nil
}

func (s *memoryRouteSessionV3) Close() { s.once.Do(s.release) }

func (s *memoryRouteSessionV3) GetAttempt(ctx context.Context) (*AttemptRecordV3, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	for _, record := range s.store.attempts {
		if record.SessionID == s.sessionID {
			copy := record
			return &copy, nil
		}
	}
	return nil, ErrSessionNotFound
}

func (s *memoryRouteSessionV3) CompareAndSwap(ctx context.Context, expected AttemptRecordV3, next TranscodeRoute) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	current, found := s.store.attempts[expected.PlaybackAttemptID]
	if !found || current.SessionID != s.sessionID || expected.SessionID != s.sessionID ||
		current.StoppedAt != nil || !current.ExpiresAt.After(time.Now()) ||
		current.CurrentPlanID != expected.CurrentPlanID || current.CurrentReplanRequestID != expected.CurrentReplanRequestID ||
		current.UserID != expected.UserID || current.ProfileID != expected.ProfileID || current.EffectiveMediaFileID != expected.EffectiveMediaFileID ||
		current.FrozenRecipe.TranscodeNodeURL != expected.FrozenRecipe.TranscodeNodeURL || current.FrozenRecipe.TranscodeTransportID != expected.FrozenRecipe.TranscodeTransportID {
		return false, nil
	}
	current.FrozenRecipe.TranscodeNodeURL, current.FrozenRecipe.TranscodeTransportID = next.NodeURL, next.TransportID
	s.store.attempts[expected.PlaybackAttemptID] = current
	return true, nil
}
