package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func realtimeResultMessage(t *testing.T, sessionID, commandID string, status playback.RealtimeResultStatus) []byte {
	t.Helper()
	data, err := json.Marshal(playback.ResultEnvelope{
		Type:      playback.RealtimeMessageTypeResult,
		CommandID: commandID,
		SessionID: sessionID,
		Status:    status,
	})
	if err != nil {
		t.Fatalf("marshal result envelope: %v", err)
	}
	return data
}

// A client that refuses a plan invalidation is left running a route the server
// has withdrawn, and its rejection already canceled the command deadline —
// so the rejection itself has to stop the session.
func TestRealtimeRejectedPlanInvalidationStopsSession(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()

	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler.rememberRealtimeCommand("cmd-1", session.ID, playback.CommandPlanInvalidated)

	if err := handler.handleRealtimeClientMessage(session.ID,
		realtimeResultMessage(t, session.ID, "cmd-1", playback.RealtimeResultStatusRejected)); err != nil {
		t.Fatalf("handleRealtimeClientMessage: %v", err)
	}

	if _, err := sessionMgr.GetSession(session.ID); err == nil {
		t.Fatal("session survived a rejected plan invalidation, want it stopped")
	}
}

// A completed invalidation means the client replanned itself: the session must
// stay alive on its replacement plan.
func TestRealtimeCompletedPlanInvalidationKeepsSession(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()

	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler.rememberRealtimeCommand("cmd-1", session.ID, playback.CommandPlanInvalidated)

	if err := handler.handleRealtimeClientMessage(session.ID,
		realtimeResultMessage(t, session.ID, "cmd-1", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatalf("handleRealtimeClientMessage: %v", err)
	}

	if _, err := sessionMgr.GetSession(session.ID); err != nil {
		t.Fatalf("GetSession after a completed replan: %v, want the session kept", err)
	}
}

// Rejecting a result has to be side-effect-free: a client claiming another
// session's command_id must not cancel that command's fallback deadline or
// drop its record, or one session could silently disarm another's recovery.
func TestRealtimeResultForOtherSessionCommandLeavesTrackerArmed(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()

	sessionA, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession A: %v", err)
	}
	sessionB, err := sessionMgr.StartSession(2, "profile-2", 200, playback.PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession B: %v", err)
	}
	if sessionA.ID == sessionB.ID {
		t.Fatal("StartSession returned colliding session IDs, want distinct sessions")
	}

	handler.rememberRealtimeCommand("cmd-1", sessionB.ID, playback.CommandPlanInvalidated)
	fired := make(chan struct{})
	handler.CommandTracker.Track("cmd-1", 20*time.Millisecond, func() { close(fired) })

	err = handler.handleRealtimeClientMessage(sessionA.ID,
		realtimeResultMessage(t, sessionA.ID, "cmd-1", playback.RealtimeResultStatusCompleted))
	if !errors.Is(err, playback.ErrInvalidRealtimePayload) {
		t.Fatalf("handleRealtimeClientMessage: %v, want ErrInvalidRealtimePayload", err)
	}

	if _, ok := handler.getRealtimeCommand("cmd-1"); !ok {
		t.Fatal("a rejected cross-session result forgot the command record, want it kept")
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("command deadline never fired, want it still armed after a rejected cross-session result")
	}
}
