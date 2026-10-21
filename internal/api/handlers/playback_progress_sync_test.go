package handlers

import (
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
