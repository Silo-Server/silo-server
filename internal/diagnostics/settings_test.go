package diagnostics

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestSeedDefaultsAdoptsConcurrentInstanceIDWinner(t *testing.T) {
	// A store that supports insert-if-absent seeds the instance ID atomically:
	// when another node wins the race, this node adopts the winning value
	// instead of overwriting it.
	store := &racingSettingsStore{
		memorySettingsStore: memorySettingsStore{values: map[string]string{}},
		concurrentWinner:    "winner-instance",
	}
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	if got := store.values[KeyServerInstanceID]; got != "winner-instance" {
		t.Fatalf("server instance id = %q, want winner-instance", got)
	}
	if store.setIfAbsentCalls != 1 {
		t.Fatalf("SetIfAbsent calls = %d, want 1", store.setIfAbsentCalls)
	}
}

func TestLoadCleanupIntervalCapsHugeValues(t *testing.T) {
	// The boundary: the largest value that still fits under the cap is accepted
	// verbatim, while anything larger (including values that would overflow
	// int64 nanoseconds when multiplied by time.Minute) clamps to the cap
	// instead of wrapping into a tiny or negative duration.
	atCap := newMemorySettingsStore(map[string]string{
		KeyCleanupIntervalMinutes: strconv.Itoa(maxCleanupIntervalMinutes),
	})
	if got := LoadCleanupInterval(context.Background(), atCap); got != maxCleanupIntervalMinutes*time.Minute {
		t.Fatalf("LoadCleanupInterval at cap = %v, want %v", got, maxCleanupIntervalMinutes*time.Minute)
	}

	for _, raw := range []string{
		strconv.Itoa(maxCleanupIntervalMinutes + 1),
		"153722868",           // wraps int64 nanoseconds without the cap
		"9223372036854775807", // math.MaxInt64 minutes
	} {
		store := newMemorySettingsStore(map[string]string{KeyCleanupIntervalMinutes: raw})
		got := LoadCleanupInterval(context.Background(), store)
		if got != maxCleanupIntervalMinutes*time.Minute {
			t.Fatalf("LoadCleanupInterval(%s) = %v, want capped %v", raw, got, maxCleanupIntervalMinutes*time.Minute)
		}
		if got <= 0 {
			t.Fatalf("LoadCleanupInterval(%s) = %v, want a positive duration", raw, got)
		}
	}
}

type memorySettingsStore struct {
	values map[string]string
}

func newMemorySettingsStore(values map[string]string) *memorySettingsStore {
	store := &memorySettingsStore{values: map[string]string{}}
	for k, v := range values {
		store.values[k] = v
	}
	return store
}

func (s *memorySettingsStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *memorySettingsStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

// failingSettingsStore returns an error when Get is called for failKey,
// simulating a transient database read failure for one setting.
type failingSettingsStore struct {
	memorySettingsStore
	failKey string
}

func (s *failingSettingsStore) Get(ctx context.Context, key string) (string, error) {
	if key == s.failKey {
		return "", errors.New("transient read failure")
	}
	return s.memorySettingsStore.Get(ctx, key)
}

// racingSettingsStore adds insert-if-absent semantics and simulates another node
// winning the instance-ID seed race via concurrentWinner.
type racingSettingsStore struct {
	memorySettingsStore
	concurrentWinner string
	setIfAbsentCalls int
}

func (s *racingSettingsStore) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.setIfAbsentCalls++
	if existing := s.values[key]; existing != "" {
		return false, nil
	}
	if s.concurrentWinner != "" {
		s.values[key] = s.concurrentWinner
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

func TestServerInstanceIDRequiresAtomicPersistence(t *testing.T) {
	if _, err := ServerInstanceID(t.Context(), newMemorySettingsStore(nil)); err == nil {
		t.Fatal("accepted store without atomic initialization")
	}
	store := &racingSettingsStore{memorySettingsStore: memorySettingsStore{values: map[string]string{}}, concurrentWinner: "winner-instance"}
	first, err := ServerInstanceID(t.Context(), store)
	if err != nil || first != "winner-instance" {
		t.Fatalf("id=%q error=%v", first, err)
	}
	again, err := ServerInstanceID(t.Context(), store)
	if err != nil || again != first || store.setIfAbsentCalls != 1 {
		t.Fatalf("repeated id=%q calls=%d error=%v", again, store.setIfAbsentCalls, err)
	}
}
