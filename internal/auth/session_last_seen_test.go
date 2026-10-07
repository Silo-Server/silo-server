package auth

import (
	"context"
	"testing"
	"time"
)

type sessionActivityCall struct {
	id                 string
	observed, deadline time.Time
}
type recordingSessionActivity struct{ calls chan sessionActivityCall }

func (u *recordingSessionActivity) UpdateLastSeen(ctx context.Context, id string, at time.Time) error {
	deadline, _ := ctx.Deadline()
	u.calls <- sessionActivityCall{id, at, deadline}
	return nil
}
func TestSessionLastSeenTrackerActivityAndThrottle(t *testing.T) {
	now := time.Now()
	u := &recordingSessionActivity{calls: make(chan sessionActivityCall, 4)}
	tracker := NewSessionLastSeenTracker(u, func() time.Time { return now })
	tracker.Touch("one")
	tracker.Touch("one")
	tracker.Touch("two")
	receive := func() sessionActivityCall {
		t.Helper()
		select {
		case c := <-u.calls:
			return c
		case <-time.After(time.Second):
			t.Fatal("activity write did not arrive")
			return sessionActivityCall{}
		}
	}
	a, b := receive(), receive()
	if a.id == b.id || !a.observed.Equal(now) || !b.observed.Equal(now) || a.deadline.IsZero() {
		t.Fatalf("unexpected activity: %+v %+v", a, b)
	}
	now = now.Add(time.Minute)
	tracker.Touch("one")
	c := receive()
	if c.id != "one" || !c.observed.Equal(now) || len(tracker.seen) != 1 {
		t.Fatalf("unexpected next interval: %+v cache=%v", c, tracker.seen)
	}
	select {
	case extra := <-u.calls:
		t.Fatalf("unthrottled write: %+v", extra)
	default:
	}
}
