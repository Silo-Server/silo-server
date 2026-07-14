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
	ExpiresAt              time.Time
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
	BeginReplan(context.Context, string, string, string, string, time.Time) (ReplanLeaseV3, error)
	CompleteReplan(context.Context, string, string, json.RawMessage, AttemptRecordV3) error
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

func (s *MemoryPlanStoreV3) AcquireSessionLock(context.Context, string) (func(), error) {
	return func() {}, nil
}

func (s *MemoryPlanStoreV3) SaveAttempt(_ context.Context, record AttemptRecordV3) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.attempts {
		if existing.PlaybackAttemptID == record.PlaybackAttemptID && existing.SessionID != record.SessionID {
			return ErrPlaybackAttemptExistsV3
		}
	}
	s.attempts[record.SessionID] = record
	return nil
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

func (s *MemoryPlanStoreV3) CompleteReplan(_ context.Context, sessionID, requestID string, response json.RawMessage, record AttemptRecordV3) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionID + ":" + requestID
	entry := s.replans[key]
	entry.completed = true
	entry.response = append(json.RawMessage(nil), response...)
	s.replans[key] = entry
	s.attempts[sessionID] = record
	return nil
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
