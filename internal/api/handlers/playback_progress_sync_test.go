package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// positionOnlySamples is how many heartbeats each test sends without a pause
// change.
const positionOnlySamples = 100

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

func TestHandleUpdateProgressSyncsSessionsOnlyOnPauseFlip(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewPlaybackHandler(manager)
	syncer := &recordingSessionSyncer{}
	handler.SessionSyncer = syncer

	send := func(position float64, paused bool) {
		t.Helper()
		body := fmt.Appendf(nil, `{"position":%g,"is_paused":%t}`, position, paused)
		req := playbackTestRequest(http.MethodPost, "/api/v1/playback/"+session.ID+"/progress", body, map[string]string{"session_id": session.ID})
		rr := httptest.NewRecorder()
		handler.HandleUpdateProgress(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("progress status = %d, body = %s", rr.Code, rr.Body.String())
		}
	}

	for i := 1; i <= positionOnlySamples; i++ {
		send(float64(i*10), false)
	}
	t.Logf("v1: %d position-only samples -> %d syncs", positionOnlySamples, syncer.calls)
	if syncer.calls != 0 {
		t.Fatalf("position-only samples synced %d times, want 0", syncer.calls)
	}

	assertProgressSyncs(t, syncer, 1, func() { send(1010, true) })
	assertProgressSyncs(t, syncer, 1, func() { send(1010, true) })
	assertProgressSyncs(t, syncer, 2, func() { send(1020, false) })
	assertProgressSyncs(t, syncer, 3, func() {
		rr := httptest.NewRecorder()
		handler.HandleStopPlayback(rr, playbackTestRequest(http.MethodDelete, "/api/v1/playback/"+session.ID, nil, map[string]string{"session_id": session.ID}))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("stop status = %d, body = %s", rr.Code, rr.Body.String())
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
