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

func TestAPIKeyLastUsedTrackerThrottlesPerKey(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	tracker := NewAPIKeyLastUsedTracker(nil, func() time.Time { return now })

	if !tracker.shouldUpdate(1) {
		t.Fatal("first use should update")
	}
	if tracker.shouldUpdate(1) {
		t.Fatal("repeat use inside the interval should be throttled")
	}
	if !tracker.shouldUpdate(2) {
		t.Fatal("a different key should update independently")
	}

	now = now.Add(apiKeyLastUsedInterval)
	if !tracker.shouldUpdate(1) {
		t.Fatal("use at the next interval should update")
	}
}

func TestAPIKeyLastUsedTrackerPrunesExpiredKeys(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	tracker := NewAPIKeyLastUsedTracker(nil, func() time.Time { return now })

	tracker.shouldUpdate(1)
	tracker.shouldUpdate(2)

	now = now.Add(apiKeyLastUsedInterval)
	tracker.shouldUpdate(3)

	if len(tracker.lastUsedAt) != 1 {
		t.Fatalf("retained entries = %d, want 1", len(tracker.lastUsedAt))
	}
	if _, ok := tracker.lastUsedAt[3]; !ok {
		t.Fatal("current key was not retained")
	}
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
