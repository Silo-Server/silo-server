package notifications

import (
	"context"
	"testing"
)

// TestInterestQueuesAreBounded: a profile queued for a rebuild many times
// rebuilds once, and neither the rebuild queue nor the item mutations grow
// past their bounds.
func TestInterestQueuesAreBounded(t *testing.T) {
	u := NewInterestUpdater(nil, nil, nil, nil)
	var rebuilt []string
	u.rebuildProfile = func(_ context.Context, _ int, profileID string) error {
		rebuilt = append(rebuilt, profileID)
		return nil
	}
	for range 3 {
		u.QueueProfileRebuild(1, "a")
	}
	for i := range interestMaxQueuedRebuilds + 10 {
		u.QueueProfileRebuild(2+i, "b")
	}
	if len(u.rebuilds) != interestMaxQueuedRebuilds {
		t.Fatalf("%d rebuilds queued, want %d", len(u.rebuilds), interestMaxQueuedRebuilds)
	}
	for range interestMaxQueuedRebuilds + 5 {
		u.runRebuild(t.Context())
	}
	a := 0
	for _, p := range rebuilt {
		if p == "a" {
			a++
		}
	}
	if a != 1 || len(rebuilt) != interestMaxQueuedRebuilds {
		t.Fatalf("rebuilt %d profiles, a %d times; want each queued profile once", len(rebuilt), a)
	}
	for i := range interestMaxPending + 10 {
		u.QueueItemMutation(1, "a", string(rune('a'+i%26))+string(rune(i)))
	}
	if len(u.pending) > interestMaxPending {
		t.Fatalf("%d mutations pending, want at most %d", len(u.pending), interestMaxPending)
	}
}
