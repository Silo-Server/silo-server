package handlers

import "testing"

// The restart-required boolean latches for the life of the process, so the
// mark count is the only signal the admin UI has that a NEW restart-required
// save happened after the banner was dismissed.
func TestServerRestartStatusMarkCount(t *testing.T) {
	tracker := NewServerRestartStatusTracker()

	if got := tracker.Snapshot().RestartMarkCount; got != 0 {
		t.Fatalf("RestartMarkCount = %d before any mark, want 0", got)
	}

	tracker.MarkRequired("ratelimit_backend")
	tracker.MarkRequired("ratelimit_backend") // same reason still counts: it is a new save
	tracker.MarkRequired("")

	snapshot := tracker.Snapshot()
	if snapshot.RestartMarkCount != 3 {
		t.Fatalf("RestartMarkCount = %d after three marks, want 3", snapshot.RestartMarkCount)
	}
	if !snapshot.RestartRequired {
		t.Fatal("RestartRequired = false after marks, want true")
	}
	if snapshot.RestartRequiredReason != "ratelimit_backend" {
		t.Fatalf("RestartRequiredReason = %q, want the last non-empty reason", snapshot.RestartRequiredReason)
	}
}
