package playback

import (
	"sync"
	"testing"
)

func TestRegisterReconstructedInsertsUnderExistingID(t *testing.T) {
	m := NewSessionManager(0, 0)
	s := &Session{ID: "sess-1", UserID: 7, MediaFileID: 3, PlayMethod: PlayTranscode}

	got := m.RegisterReconstructed(s)
	if got != s {
		t.Fatal("expected the registered session to be returned")
	}
	if got.StartedAt.IsZero() || got.LastActivityAt.IsZero() {
		t.Error("timestamps should be armed on register")
	}

	// The session is now retrievable under its original ID (no new UUID minted).
	back, err := m.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession after reconstruct: %v", err)
	}
	if back.UserID != 7 {
		t.Errorf("UserID = %d, want 7", back.UserID)
	}
}

func TestRegisterReconstructedYieldsToExisting(t *testing.T) {
	m := NewSessionManager(0, 0)
	first := &Session{ID: "dup", UserID: 1}
	m.RegisterReconstructed(first)

	// A concurrent reconstruct with the same ID must return the existing one.
	second := &Session{ID: "dup", UserID: 999}
	got := m.RegisterReconstructed(second)
	if got != first {
		t.Fatal("expected existing session to win the race")
	}
	if got.UserID != 1 {
		t.Errorf("UserID = %d, want 1 (existing preserved)", got.UserID)
	}
}

func TestRegisterReconstructedConcurrent(t *testing.T) {
	m := NewSessionManager(0, 0)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RegisterReconstructed(&Session{ID: "race", UserID: 5})
		}()
	}
	wg.Wait()
	if _, err := m.GetSession("race"); err != nil {
		t.Fatalf("session should exist after concurrent registers: %v", err)
	}
}
