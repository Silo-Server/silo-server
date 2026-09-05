package playback

import (
	"context"
	"testing"
	"time"
)

func TestCompatExpiryRechecksLocalActivityAfterSharedRead(t *testing.T) {
	manager := NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile", 1, PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	session.IsJellyfinCompat = true
	old := time.Now().Add(-time.Hour)
	session.LastActivityAt = old
	manager.SetCompatActivityReader(func(context.Context, []Session) (map[string]time.Time, error) {
		// This call proves the shared reader runs outside the manager lock.
		if err := manager.UpdateProgress(session.ID, 42, true); err != nil {
			t.Fatal(err)
		}
		return map[string]time.Time{session.ID: old}, nil
	})
	manager.SetCompatExpiryClaimer(func(context.Context, []SessionExpiryCandidate) (map[string]bool, error) {
		t.Fatal("claimed newly active session")
		return nil, nil
	})
	if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 0 {
		t.Fatal("expired concurrent local activity")
	}
	got, err := manager.GetSession(session.ID)
	if err != nil || got.Position != 42 || !got.IsPaused || !got.LastActivityAt.After(old) {
		t.Fatalf("session=%+v err=%v", got, err)
	}
}

func TestCompatExpiryClaimIsBoundedAndPreservesOverflow(t *testing.T) {
	manager := NewSessionManager(0, 0)
	for range 257 {
		session, err := manager.StartSession(1, "profile", 1, PlayDirect, false)
		if err != nil {
			t.Fatal(err)
		}
		session.IsJellyfinCompat = true
		session.LastActivityAt = time.Now().Add(-time.Hour)
	}
	manager.SetCompatExpiryClaimer(func(ctx context.Context, candidates []SessionExpiryCandidate) (map[string]bool, error) {
		if len(candidates) != 256 {
			t.Fatalf("batch=%d", len(candidates))
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 250*time.Millisecond {
			t.Fatal("missing bounded claim deadline")
		}
		result := make(map[string]bool, len(candidates))
		for _, candidate := range candidates {
			result[candidate.ID] = true
		}
		return result, nil
	})
	if expired := manager.CleanInactive(time.Minute, time.Minute); len(expired) != 256 {
		t.Fatalf("expired=%d", len(expired))
	}
	if len(manager.AllSessions()) != 1 {
		t.Fatal("unclaimed overflow session removed")
	}
}
