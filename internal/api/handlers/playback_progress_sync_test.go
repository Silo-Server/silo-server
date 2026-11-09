package handlers

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// positionOnlySamples is how many heartbeats each test sends without a pause
// change.
const positionOnlySamples = 2

// A progress heartbeat must not reconcile the node's whole session snapshot.
// The periodic reconcile tick carries position; a pause flip and a stop still
// sync at once.
func TestApplyProgressV2SyncsSessionsOnlyOnPauseFlip(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	syncer := &recordingSessionSyncer{}
	f.handler.SessionSyncer = syncer

	sequence := int64(0)
	send := func(position float64, paused bool) {
		t.Helper()
		sequence++
		command := PlaybackProgressCommand{Sequence: sequence, Position: position, IsPaused: paused}
		if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, command); err != nil {
			t.Fatalf("progress %d: %v", sequence, err)
		}
	}

	for i := 1; i <= positionOnlySamples; i++ {
		send(float64(i*10), false)
	}
	t.Logf("v2: %d position-only samples -> %d syncs", positionOnlySamples, syncer.calls)
	if syncer.calls != 0 {
		t.Fatalf("position-only samples synced %d times, want 0", syncer.calls)
	}

	assertProgressSyncs(t, syncer, 1, func() { send(1010, true) })
	assertProgressSyncs(t, syncer, 1, func() { send(1010, true) })
	assertProgressSyncs(t, syncer, 2, func() { send(1020, false) })
	assertProgressSyncs(t, syncer, 3, func() {
		if _, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()}); err != nil {
			t.Fatalf("stop: %v", err)
		}
	})
}

func assertProgressSyncs(t *testing.T, syncer *recordingSessionSyncer, want int, act func()) {
	t.Helper()
	act()
	if syncer.calls != want {
		t.Fatalf("syncs = %d, want %d", syncer.calls, want)
	}
}

// The buffered position a client reports rides on the live session: each
// applied sample replaces it, one without it clears it, a stale sample cannot
// overwrite it, and an impossible value is refused.
func TestApplyProgressV2KeepsTheLatestClientBuffer(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	for _, step := range []struct {
		sequence int64
		buffered *float64
		want     *float64
	}{
		{sequence: 1, buffered: new(30.0), want: new(30.0)},
		{sequence: 2, want: nil},
		{sequence: 3, buffered: new(90.0), want: new(90.0)},
		{sequence: 2, buffered: new(999.0), want: new(90.0)},
	} {
		command := PlaybackProgressCommand{Sequence: step.sequence, Position: 10, BufferedThrough: step.buffered}
		if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, command); err != nil {
			t.Fatalf("progress %d: %v", step.sequence, err)
		}
		session, err := f.manager.GetSession(f.session.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := session.ClientBufferedThroughSeconds()
		if want := step.want; ok != (want != nil) || (ok && got != *want) {
			t.Fatalf("after sequence %d: buffered = (%v, %t), want present=%t %v", step.sequence, got, ok, want != nil, step.buffered)
		}
	}

	_, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 4, Position: 10, BufferedThrough: new(-1.0)})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
}
