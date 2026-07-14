package playback

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrIdempotencyKeyReusedV3 = errors.New("idempotency key reused")
var ErrPlaybackAttemptExistsV3 = errors.New("playback attempt already exists")
var ErrStaleReplanLeaseV3 = errors.New("stale replan lease")

// ErrReplanSupersededV3 means a CompleteReplan lost the revision compare: a
// newer replan already moved the attempt past the caller's base revision.
var ErrReplanSupersededV3 = errors.New("replan superseded")

type AttemptRecordV3 struct {
	PlaybackAttemptID      string
	SessionID              string
	UserID                 int
	ProfileID              string
	RequestedMediaFileID   int
	EffectiveMediaFileID   int
	CurrentPlanID          string
	CurrentReplanRequestID string
	CurrentPlan            PlanV3
	NormalizedRequest      StartRequestV3
	// RequestDigest fingerprints the normalized start request so an attempt-ID
	// reused with different input is a detectable idempotency violation rather
	// than a silent replay of the old plan.
	RequestDigest string
	ExpiresAt     time.Time
}

// AttemptIdentityV3 carries only the ownership columns of an attempt so
// per-event authorization checks avoid decoding the plan and request JSONB.
type AttemptIdentityV3 struct {
	PlaybackAttemptID string
	SessionID         string
	UserID            int
	ProfileID         string
}

type RouteEventRecordV3 struct {
	RouteEventV3
	UserID        int
	ProfileID     string
	ClientName    string
	ClientVersion string
	ClientModel   string
}

type ReplanLeaseStateV3 string

const (
	ReplanLeaseOwnedV3     ReplanLeaseStateV3 = "owned"
	ReplanLeaseInFlightV3  ReplanLeaseStateV3 = "in_flight"
	ReplanLeaseCompletedV3 ReplanLeaseStateV3 = "completed"
)

type ReplanLeaseV3 struct {
	State    ReplanLeaseStateV3
	Response json.RawMessage
}

type PlanStoreV3 interface {
	AcquireSessionLock(context.Context, string) (func(), error)
	SaveAttempt(context.Context, AttemptRecordV3) error
	GetAttempt(context.Context, string) (*AttemptRecordV3, error)
	GetAttemptByPlaybackAttemptID(context.Context, string) (*AttemptRecordV3, error)
	GetAttemptIdentity(context.Context, string) (*AttemptIdentityV3, error)
	GetAttemptIdentityByPlaybackAttemptID(context.Context, string) (*AttemptIdentityV3, error)
	BeginReplan(context.Context, string, string, string, string, time.Time) (ReplanLeaseV3, error)
	// CompleteReplan commits a replan atomically; the attempt row is only
	// updated while its current_replan_request_id still equals the caller's
	// base revision, otherwise ErrReplanSupersededV3 is returned.
	CompleteReplan(ctx context.Context, sessionID, requestID, baseReplanRequestID string, response json.RawMessage, record AttemptRecordV3) error
	RecordRouteEvent(context.Context, RouteEventRecordV3) error
	CleanupExpired(context.Context, time.Time) (int64, error)
}

type memoryReplanV3 struct {
	digest    string
	base      string
	lease     time.Time
	completed bool
	response  json.RawMessage
}

type MemoryPlanStoreV3 struct {
	mu       sync.Mutex
	attempts map[string]AttemptRecordV3
	replans  map[string]memoryReplanV3
	events   []RouteEventRecordV3
}

func NewMemoryPlanStoreV3() *MemoryPlanStoreV3 {
	return &MemoryPlanStoreV3{attempts: make(map[string]AttemptRecordV3), replans: make(map[string]memoryReplanV3)}
}

// AcquireSessionLock is deliberately a no-op. The store lock exists to
// serialize replans across processes sharing one PostgreSQL database; the
// memory store only ever backs a single-process, DB-less deployment, where
// the handler's own per-session replan mutex already provides the same
// serialization before this lock is taken.
func (s *MemoryPlanStoreV3) AcquireSessionLock(context.Context, string) (func(), error) {
	return func() {}, nil
}

func (s *MemoryPlanStoreV3) SaveAttempt(_ context.Context, record AttemptRecordV3) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// Expired rows are replaceable, mirroring the Postgres pre-delete: they
	// linger until the hourly cleanup and must not wedge a legitimate retry.
	for sessionID, existing := range s.attempts {
		if existing.ExpiresAt.After(now) {
			continue
		}
		if existing.PlaybackAttemptID == record.PlaybackAttemptID || sessionID == record.SessionID {
			s.deleteAttemptLocked(sessionID)
		}
	}
	for sessionID, existing := range s.attempts {
		if existing.PlaybackAttemptID != record.PlaybackAttemptID && sessionID != record.SessionID {
			continue
		}
		if existing.PlaybackAttemptID == record.PlaybackAttemptID &&
			existing.RequestDigest != "" && record.RequestDigest != "" && existing.RequestDigest != record.RequestDigest {
			return ErrIdempotencyKeyReusedV3
		}
		return ErrPlaybackAttemptExistsV3
	}
	s.attempts[record.SessionID] = record
	return nil
}

// ReplaceAttempt overwrites a session's attempt record unconditionally. It is
// not part of PlanStoreV3: durable stores treat attempts as insert-once and
// replan-updated, so only in-memory test setups may rewrite one in place.
func (s *MemoryPlanStoreV3) ReplaceAttempt(_ context.Context, record AttemptRecordV3) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[record.SessionID] = record
}

func (s *MemoryPlanStoreV3) deleteAttemptLocked(sessionID string) {
	delete(s.attempts, sessionID)
	for key := range s.replans {
		if strings.HasPrefix(key, sessionID+":") {
			delete(s.replans, key)
		}
	}
}

func (s *MemoryPlanStoreV3) GetAttemptByPlaybackAttemptID(_ context.Context, attemptID string) (*AttemptRecordV3, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.attempts {
		if record.PlaybackAttemptID == attemptID && record.ExpiresAt.After(time.Now()) {
			copy := record
			return &copy, nil
		}
	}
	return nil, ErrSessionNotFound
}

func (s *MemoryPlanStoreV3) GetAttempt(_ context.Context, sessionID string) (*AttemptRecordV3, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.attempts[sessionID]
	if !ok || !record.ExpiresAt.After(time.Now()) {
		return nil, ErrSessionNotFound
	}
	copy := record
	return &copy, nil
}

func (s *MemoryPlanStoreV3) BeginReplan(_ context.Context, sessionID, requestID, digest, baseReplanRequestID string, leaseUntil time.Time) (ReplanLeaseV3, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + ":" + requestID
	existing, ok := s.replans[key]
	if !ok {
		s.replans[key] = memoryReplanV3{digest: digest, base: baseReplanRequestID, lease: leaseUntil}
		return ReplanLeaseV3{State: ReplanLeaseOwnedV3}, nil
	}
	if existing.digest != digest {
		return ReplanLeaseV3{}, ErrIdempotencyKeyReusedV3
	}
	if existing.completed {
		return ReplanLeaseV3{State: ReplanLeaseCompletedV3, Response: append(json.RawMessage(nil), existing.response...)}, nil
	}
	if time.Now().Before(existing.lease) {
		return ReplanLeaseV3{State: ReplanLeaseInFlightV3}, nil
	}
	if existing.base != baseReplanRequestID {
		return ReplanLeaseV3{}, ErrStaleReplanLeaseV3
	}
	existing.lease = leaseUntil
	s.replans[key] = existing
	return ReplanLeaseV3{State: ReplanLeaseOwnedV3}, nil
}

func (s *MemoryPlanStoreV3) CompleteReplan(_ context.Context, sessionID, requestID, baseReplanRequestID string, response json.RawMessage, record AttemptRecordV3) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.attempts[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if existing.CurrentReplanRequestID != baseReplanRequestID {
		return ErrReplanSupersededV3
	}
	key := sessionID + ":" + requestID
	entry, ok := s.replans[key]
	if !ok {
		return ErrSessionNotFound
	}
	entry.completed = true
	entry.response = append(json.RawMessage(nil), response...)
	s.replans[key] = entry
	s.attempts[sessionID] = record
	return nil
}

func (s *MemoryPlanStoreV3) GetAttemptIdentity(ctx context.Context, sessionID string) (*AttemptIdentityV3, error) {
	record, err := s.GetAttempt(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &AttemptIdentityV3{PlaybackAttemptID: record.PlaybackAttemptID, SessionID: record.SessionID, UserID: record.UserID, ProfileID: record.ProfileID}, nil
}

func (s *MemoryPlanStoreV3) GetAttemptIdentityByPlaybackAttemptID(ctx context.Context, attemptID string) (*AttemptIdentityV3, error) {
	record, err := s.GetAttemptByPlaybackAttemptID(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	return &AttemptIdentityV3{PlaybackAttemptID: record.PlaybackAttemptID, SessionID: record.SessionID, UserID: record.UserID, ProfileID: record.ProfileID}, nil
}

func (s *MemoryPlanStoreV3) RecordRouteEvent(_ context.Context, record RouteEventRecordV3) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, record)
	return nil
}

func (s *MemoryPlanStoreV3) CleanupExpired(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for sessionID, record := range s.attempts {
		if !record.ExpiresAt.After(now) {
			delete(s.attempts, sessionID)
			for key := range s.replans {
				if strings.HasPrefix(key, sessionID+":") {
					delete(s.replans, key)
				}
			}
			count++
		}
	}
	return count, nil
}
