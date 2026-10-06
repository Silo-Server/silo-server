package playback

import (
	"context"
	"errors"
	"testing"
)

// TestFinishSessionRunsFinishHooksOnce proves the finish hooks fire only for
// the call that removed the session: a retried finish, and a StopSession used
// for a mid-play replacement, run none.
func TestFinishSessionRunsFinishHooksOnce(t *testing.T) {
	m := NewSessionManager(0, 0)
	var finished []Session
	m.AddFinishHook(func(_ context.Context, s *Session) {
		finished = append(finished, *s)
	})

	m.RegisterReconstructed(&Session{ID: "played", UserID: 1, ProfileID: "p"})
	if err := m.UpdateProgress("played", 1234, false); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	if err := m.FinishSession(context.Background(), "played"); err != nil {
		t.Fatalf("FinishSession: %v", err)
	}
	if _, err := m.GetSession("played"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession after finish: err = %v, want ErrSessionNotFound", err)
	}
	if err := m.FinishSession(context.Background(), "played"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("retried FinishSession: err = %v, want ErrSessionNotFound", err)
	}

	m.RegisterReconstructed(&Session{ID: "replaced", UserID: 1, ProfileID: "p"})
	if err := m.StopSession("replaced"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	if len(finished) != 1 {
		t.Fatalf("finish hook calls = %d, want 1", len(finished))
	}
	if finished[0].ID != "played" || finished[0].Position != 1234 {
		t.Fatalf("finished session = %q at %v, want played at 1234", finished[0].ID, finished[0].Position)
	}
}
