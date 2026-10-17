package playback

import (
	"context"
	"slices"
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
	// WithLiveRoute runs a bounded callback only while the exact predecessor
	// remains live. Row ownership excludes StopAttempt through the callback;
	// implementations never hold a store-wide mutex across external I/O.
	WithLiveRoute(context.Context, AttemptRecordV3, func(context.Context) error) (bool, error)
	Close()
}

type memoryRouteSessionV3 struct {
	store     *MemoryPlanStoreV3
	sessionID string
	release   func()
	once      sync.Once
}

// Save/replace can also retire an expired attempt whose session identity
// changed. Lock every affected session in order, then recheck the snapshot.
func (s *MemoryPlanStoreV3) lockAttemptRows(ctx context.Context, record AttemptRecordV3) (func(), error) {
	for {
		ids := []string{record.SessionID}
		s.mu.Lock()
		for _, current := range s.attempts {
			if current.PlaybackAttemptID == record.PlaybackAttemptID || current.SessionID == record.SessionID {
				ids = append(ids, current.SessionID)
			}
		}
		s.mu.Unlock()
		slices.Sort(ids)
		ids = slices.Compact(ids)
		var releases []func()
		release := func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		}
		for _, id := range ids {
			unlock, err := s.routeRows.Acquire(ctx, id)
			if err != nil {
				release()
				return nil, err
			}
			releases = append(releases, unlock)
		}
		stable := true
		s.mu.Lock()
		for _, current := range s.attempts {
			if (current.PlaybackAttemptID == record.PlaybackAttemptID || current.SessionID == record.SessionID) && !slices.Contains(ids, current.SessionID) {
				stable = false
				break
			}
		}
		if stable {
			return func() { s.mu.Unlock(); release() }, nil
		}
		s.mu.Unlock()
		release()
	}
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
			cloned := record
			return &cloned, nil
		}
	}
	return nil, ErrSessionNotFound
}

func (s *memoryRouteSessionV3) CompareAndSwap(ctx context.Context, expected AttemptRecordV3, next TranscodeRoute) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	release, err := s.store.routeRows.Acquire(ctx, s.sessionID)
	if err != nil {
		return false, err
	}
	defer release()
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

// SameLiveRouteV3 compares authority, not progress or unrelated telemetry.
func SameLiveRouteV3(current, expected *AttemptRecordV3) bool {
	return current != nil && expected != nil && current.SessionID == expected.SessionID &&
		NewRouteMoveIdentityV3(expected, expected.FrozenRecipe.TranscodeTransportID).Matches(current, current.FrozenRecipe.TranscodeTransportID) &&
		current.FrozenRecipe.TranscodeNodeURL == expected.FrozenRecipe.TranscodeNodeURL &&
		current.StoppedAt == nil && current.ExpiresAt.After(time.Now())
}

func (s *memoryRouteSessionV3) WithLiveRoute(ctx context.Context, expected AttemptRecordV3, run func(context.Context) error) (bool, error) {
	if expected.SessionID != s.sessionID {
		return false, nil
	}
	release, err := s.store.routeRows.Acquire(ctx, s.sessionID)
	if err != nil {
		return false, err
	}
	defer release()
	current, err := s.GetAttempt(ctx)
	if err != nil {
		return false, err
	}
	if !SameLiveRouteV3(current, &expected) {
		return false, nil
	}
	callbackCtx, cancel := context.WithDeadline(ctx, current.ExpiresAt)
	defer cancel()
	return true, run(callbackCtx)
}
