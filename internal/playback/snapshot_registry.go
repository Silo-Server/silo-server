package playback

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSnapshotUnknown          = errors.New("snapshot unknown")
	ErrSnapshotIncomplete       = errors.New("snapshot incomplete")
	ErrSnapshotStale            = errors.New("snapshot stale")
	ErrSnapshotIdentityMismatch = errors.New("snapshot session identity mismatch")
	ErrInvalidSnapshotIdentity  = errors.New("invalid snapshot session identity")
	ErrSnapshotCapacity         = errors.New("snapshot identity capacity exceeded")
)

// SnapshotSessionIdentity is the only session data retained for termination
// authorization. SnapshotRegistry deliberately does not retain snapshot rows.
type SnapshotSessionIdentity struct {
	SessionID  string
	Generation string
}

type registeredSnapshot struct {
	generatedAt time.Time
	expiresAt   time.Time
	identities  map[string]string
}

// SnapshotRegistry is a bounded in-memory registry of complete, recent admin
// snapshots. It binds a snapshot ID to exact generation-bearing identities.
type SnapshotRegistry struct {
	mu            sync.Mutex
	entries       map[string]registeredSnapshot
	max           int
	maxIdentities int
	identityCount int
	freshness     time.Duration
	now           func() time.Time
}

func NewSnapshotRegistry(maxEntries int, freshness time.Duration, identityBudget ...int) *SnapshotRegistry {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	if freshness <= 0 {
		freshness = time.Second
	}
	maxIdentities := 65536
	if len(identityBudget) > 0 && identityBudget[0] > 0 {
		maxIdentities = identityBudget[0]
	}
	return &SnapshotRegistry{
		entries:       make(map[string]registeredSnapshot),
		max:           maxEntries,
		maxIdentities: maxIdentities,
		freshness:     freshness,
		now:           time.Now,
	}
}

// SetClock replaces the clock for deterministic tests. Configure it before
// concurrent use.
func (r *SnapshotRegistry) SetClock(now func() time.Time) {
	if r == nil || now == nil {
		return
	}
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

func (r *SnapshotRegistry) Store(snapshotID string, generatedAt time.Time, complete bool, sessions []SnapshotSessionIdentity) error {
	if r == nil {
		return ErrSnapshotUnknown
	}
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" || generatedAt.IsZero() {
		return fmt.Errorf("%w: snapshot ID and generation time are required", ErrSnapshotUnknown)
	}
	if !complete {
		return ErrSnapshotIncomplete
	}
	identities := make(map[string]string, len(sessions))
	for _, identity := range sessions {
		sessionID := strings.TrimSpace(identity.SessionID)
		generation := strings.TrimSpace(identity.Generation)
		parsed, err := uuid.Parse(generation)
		if sessionID == "" || err != nil || parsed == uuid.Nil {
			return fmt.Errorf("%w: complete snapshots require non-sentinel session identities", ErrInvalidSnapshotIdentity)
		}
		if previous, exists := identities[sessionID]; exists && previous != parsed.String() {
			return fmt.Errorf("%w: duplicate session ID has multiple generations", ErrInvalidSnapshotIdentity)
		}
		identities[sessionID] = parsed.String()
	}
	if len(identities) > r.maxIdentities {
		return ErrSnapshotCapacity
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.pruneLocked(now)
	if previous, exists := r.entries[snapshotID]; exists {
		r.identityCount -= len(previous.identities)
		delete(r.entries, snapshotID)
	}
	for len(r.entries) >= r.max || r.identityCount+len(identities) > r.maxIdentities {
		r.evictOldestLocked()
	}
	r.entries[snapshotID] = registeredSnapshot{
		generatedAt: generatedAt.UTC(),
		expiresAt:   generatedAt.UTC().Add(r.freshness),
		identities:  identities,
	}
	r.identityCount += len(identities)
	return nil
}

func (r *SnapshotRegistry) Validate(snapshotID string, identity SnapshotSessionIdentity) error {
	if r == nil {
		return ErrSnapshotUnknown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	snapshot, ok := r.entries[strings.TrimSpace(snapshotID)]
	if !ok {
		r.pruneLocked(now)
		return ErrSnapshotUnknown
	}
	if !now.Before(snapshot.expiresAt) {
		r.identityCount -= len(snapshot.identities)
		delete(r.entries, strings.TrimSpace(snapshotID))
		return ErrSnapshotStale
	}
	want, ok := snapshot.identities[strings.TrimSpace(identity.SessionID)]
	parsed, err := uuid.Parse(strings.TrimSpace(identity.Generation))
	if !ok || err != nil || parsed == uuid.Nil || want != parsed.String() {
		return ErrSnapshotIdentityMismatch
	}
	return nil
}

func (r *SnapshotRegistry) IdentityCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now().UTC())
	return r.identityCount
}

func (r *SnapshotRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now().UTC())
	return len(r.entries)
}

func (r *SnapshotRegistry) pruneLocked(now time.Time) {
	for id, entry := range r.entries {
		if !now.Before(entry.expiresAt) {
			r.identityCount -= len(entry.identities)
			delete(r.entries, id)
		}
	}
}

func (r *SnapshotRegistry) evictOldestLocked() {
	var oldestID string
	var oldestAt time.Time
	for id, entry := range r.entries {
		if oldestID == "" || entry.generatedAt.Before(oldestAt) || (entry.generatedAt.Equal(oldestAt) && id < oldestID) {
			oldestID = id
			oldestAt = entry.generatedAt
		}
	}
	if oldestID != "" {
		r.identityCount -= len(r.entries[oldestID].identities)
		delete(r.entries, oldestID)
	}
}
