package playback

import (
	"strconv"
	"testing"
	"time"
)

func TestSessionLifecycleLockEntriesDrain(t *testing.T) {
	m := NewSessionManager(0, 0)
	releaseFirst := m.acquireSessionLifecycleLock("session-1")
	if got := len(m.sessionLifecycleLocks); got != 1 {
		t.Fatalf("lifecycle lock entries while held = %d, want 1", got)
	}
	releaseFirst()
	if got := len(m.sessionLifecycleLocks); got != 0 {
		t.Fatalf("lifecycle lock entries after release = %d, want 0", got)
	}
}

func TestEndedGenerationFallbackPrunesExpiredEntries(t *testing.T) {
	m := NewSessionManager(0, 0)
	now := time.Now().UTC()
	m.endedGenerations[sessionGenerationKey{sessionID: "expired", generation: "g1"}] = now.Add(-time.Second)
	m.endedGenerations[sessionGenerationKey{sessionID: "live", generation: "g2"}] = now.Add(time.Hour)

	ended, err := m.wasSessionGenerationEnded(t.Context(), "missing", "g3", now)
	if err != nil {
		t.Fatalf("wasSessionGenerationEnded: %v", err)
	}
	if ended {
		t.Fatal("missing generation reported ended")
	}
	if got := len(m.endedGenerations); got != 1 {
		t.Fatalf("fallback tombstones after prune = %d, want 1", got)
	}
}

func TestEndedGenerationFallbackHardCapEvictsEarliestExpiry(t *testing.T) {
	m := NewSessionManager(0, 0)
	now := time.Now().UTC()
	earliest := sessionGenerationKey{sessionID: "session-0", generation: "generation-0"}
	for i := 0; i < maxInMemorySessionGenerationTombstones; i++ {
		key := sessionGenerationKey{sessionID: "session-" + strconv.Itoa(i+1), generation: "generation"}
		m.endedGenerations[key] = now.Add(time.Duration(i+1) * time.Second)
	}
	m.endedGenerations[earliest] = now.Add(time.Millisecond)
	delete(m.endedGenerations, sessionGenerationKey{sessionID: "session-" + strconv.Itoa(maxInMemorySessionGenerationTombstones), generation: "generation"})
	if got := len(m.endedGenerations); got != maxInMemorySessionGenerationTombstones {
		t.Fatalf("fixture entries = %d, want cap %d", got, maxInMemorySessionGenerationTombstones)
	}

	inserted := sessionGenerationKey{sessionID: "new", generation: "generation-new"}
	m.rememberEndedGenerationLocked(inserted, now.Add(MaxTokenTTL), now)
	if got := len(m.endedGenerations); got != maxInMemorySessionGenerationTombstones {
		t.Fatalf("fallback entries after insert = %d, want hard cap %d", got, maxInMemorySessionGenerationTombstones)
	}
	if _, ok := m.endedGenerations[earliest]; ok {
		t.Fatal("earliest-expiring tombstone was not evicted at capacity")
	}
	if _, ok := m.endedGenerations[inserted]; !ok {
		t.Fatal("new tombstone was not inserted at capacity")
	}
}

func TestEndedGenerationFallbackPutPrunesExpiredEntries(t *testing.T) {
	m := NewSessionManager(0, 0)
	now := time.Now().UTC()
	expired := sessionGenerationKey{sessionID: "expired", generation: "generation-old"}
	m.endedGenerations[expired] = now.Add(-time.Second)
	inserted := sessionGenerationKey{sessionID: "new", generation: "generation-new"}
	m.rememberEndedGenerationLocked(inserted, now.Add(time.Hour), now)
	if _, ok := m.endedGenerations[expired]; ok {
		t.Fatal("put did not prune expired tombstone")
	}
	if _, ok := m.endedGenerations[inserted]; !ok {
		t.Fatal("put did not retain new tombstone")
	}
}
