package auth

import (
	"context"
	"testing"
	"time"
)

type recordingLastUsedUpdater struct {
	calls chan lastUsedCall
}

type lastUsedCall struct {
	id       int64
	deadline time.Time
}

func (u *recordingLastUsedUpdater) UpdateLastUsed(ctx context.Context, id int64) error {
	deadline, _ := ctx.Deadline()
	u.calls <- lastUsedCall{id: id, deadline: deadline}
	return nil
}

func TestAPIKeyLastUsedTrackerAddsWriteDeadline(t *testing.T) {
	updater := &recordingLastUsedUpdater{calls: make(chan lastUsedCall, 1)}
	tracker := NewAPIKeyLastUsedTracker(updater, nil)
	tracker.Touch(42)

	select {
	case call := <-updater.calls:
		if call.id != 42 {
			t.Fatalf("key id = %d, want 42", call.id)
		}
		if call.deadline.IsZero() {
			t.Fatal("last-used update context has no deadline")
		}
		remaining := time.Until(call.deadline)
		if remaining <= 0 || remaining > apiKeyLastUsedTimeout {
			t.Fatalf("deadline after %s, want within (0, %s]", remaining, apiKeyLastUsedTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for last-used update")
	}
}
