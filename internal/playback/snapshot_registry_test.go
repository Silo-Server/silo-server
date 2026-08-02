package playback_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestTerminateSessionSnapshotRegistryRequiresCompleteFreshExactIdentity(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	registry := playback.NewSnapshotRegistry(4, 45*time.Second)
	registry.SetClock(func() time.Time { return now })
	identity := playback.SnapshotSessionIdentity{
		SessionID:  "session-1",
		Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a",
	}

	if err := registry.Store("snapshot-complete", now, true, []playback.SnapshotSessionIdentity{identity}); err != nil {
		t.Fatalf("Store complete: %v", err)
	}
	if err := registry.Validate("snapshot-complete", identity); err != nil {
		t.Fatalf("Validate exact identity: %v", err)
	}
	if err := registry.Store("snapshot-incomplete", now, false, []playback.SnapshotSessionIdentity{identity}); !errors.Is(err, playback.ErrSnapshotIncomplete) {
		t.Fatalf("Store incomplete error = %v, want ErrSnapshotIncomplete", err)
	}
	if err := registry.Validate("snapshot-incomplete", identity); !errors.Is(err, playback.ErrSnapshotUnknown) {
		t.Fatalf("Validate incomplete snapshot error = %v, want ErrSnapshotUnknown", err)
	}
	if err := registry.Validate("snapshot-unknown", identity); !errors.Is(err, playback.ErrSnapshotUnknown) {
		t.Fatalf("Validate unknown snapshot error = %v, want ErrSnapshotUnknown", err)
	}
	if err := registry.Validate("snapshot-complete", playback.SnapshotSessionIdentity{SessionID: identity.SessionID, Generation: "7d556533-6ed8-4593-a31e-52c34f0a5cf4"}); !errors.Is(err, playback.ErrSnapshotIdentityMismatch) {
		t.Fatalf("Validate mismatched generation error = %v, want ErrSnapshotIdentityMismatch", err)
	}

	now = now.Add(45 * time.Second)
	if err := registry.Validate("snapshot-complete", identity); !errors.Is(err, playback.ErrSnapshotStale) {
		t.Fatalf("Validate stale snapshot error = %v, want ErrSnapshotStale", err)
	}
}

func TestTerminateSessionSnapshotRegistryRejectsUnsafeIdentitiesAndIsBounded(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	registry := playback.NewSnapshotRegistry(2, time.Minute)
	registry.SetClock(func() time.Time { return now })

	unsafe := []playback.SnapshotSessionIdentity{
		{SessionID: "", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a"},
		{SessionID: "session", Generation: ""},
		{SessionID: "session", Generation: playback.LegacySessionGenerationSentinel},
	}
	for _, identity := range unsafe {
		if err := registry.Store("unsafe", now, true, []playback.SnapshotSessionIdentity{identity}); !errors.Is(err, playback.ErrInvalidSnapshotIdentity) {
			t.Fatalf("Store(%+v) error = %v, want ErrInvalidSnapshotIdentity", identity, err)
		}
	}

	identity := playback.SnapshotSessionIdentity{SessionID: "session", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a"}
	for _, id := range []string{"snapshot-1", "snapshot-2", "snapshot-3"} {
		if err := registry.Store(id, now, true, []playback.SnapshotSessionIdentity{identity}); err != nil {
			t.Fatalf("Store(%q): %v", id, err)
		}
		now = now.Add(time.Second)
	}
	if got := registry.Len(); got != 2 {
		t.Fatalf("registry size = %d, want 2", got)
	}
	if err := registry.Validate("snapshot-1", identity); !errors.Is(err, playback.ErrSnapshotUnknown) {
		t.Fatalf("oldest snapshot error = %v, want bounded eviction", err)
	}
}

func TestTerminateSessionSnapshotRegistryEnforcesTotalIdentityBudget(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	registry := playback.NewSnapshotRegistry(8, time.Minute, 2)
	registry.SetClock(func() time.Time { return now })
	identity := func(id string) playback.SnapshotSessionIdentity {
		return playback.SnapshotSessionIdentity{SessionID: id, Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a"}
	}
	if err := registry.Store("snapshot-1", now, true, []playback.SnapshotSessionIdentity{identity("session-1")}); err != nil {
		t.Fatalf("Store snapshot-1: %v", err)
	}
	now = now.Add(time.Second)
	if err := registry.Store("snapshot-2", now, true, []playback.SnapshotSessionIdentity{identity("session-2")}); err != nil {
		t.Fatalf("Store snapshot-2: %v", err)
	}
	now = now.Add(time.Second)
	if err := registry.Store("snapshot-3", now, true, []playback.SnapshotSessionIdentity{identity("session-3")}); err != nil {
		t.Fatalf("Store snapshot-3: %v", err)
	}
	if err := registry.Validate("snapshot-1", identity("session-1")); !errors.Is(err, playback.ErrSnapshotUnknown) {
		t.Fatalf("oldest snapshot error = %v, want identity-budget eviction", err)
	}
	if got := registry.IdentityCount(); got != 2 {
		t.Fatalf("retained identities = %d, want 2", got)
	}
	if err := registry.Store("oversized", now, true, []playback.SnapshotSessionIdentity{identity("a"), identity("b"), identity("c")}); !errors.Is(err, playback.ErrSnapshotCapacity) {
		t.Fatalf("oversized snapshot error = %v, want ErrSnapshotCapacity", err)
	}
	if got := registry.IdentityCount(); got != 2 {
		t.Fatalf("oversized rejection changed retained identities = %d", got)
	}
}
