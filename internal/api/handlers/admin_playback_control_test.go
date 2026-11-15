package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type adminPlaybackControlTestConn struct {
	messages []any
}

func (c *adminPlaybackControlTestConn) WriteJSON(v any) error {
	c.messages = append(c.messages, v)
	return nil
}

func newAdminPlaybackControlTestHandler(t *testing.T) (*AdminPlaybackControlHandler, *playback.SessionManager, *playback.RealtimeHub, *playback.Session) {
	t.Helper()

	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	realtimeHub := playback.NewRealtimeHub()
	commandTracker := playback.NewCommandTracker()
	t.Cleanup(commandTracker.Close)

	playbackHandler := NewPlaybackHandler(sessionMgr)
	playbackHandler.RealtimeHub = realtimeHub
	playbackHandler.CommandTracker = commandTracker
	playbackHandler.CommandDispatcher = playback.NewCommandDispatcher(sessionMgr, realtimeHub, commandTracker)

	return NewAdminPlaybackControlHandler(playbackHandler), sessionMgr, realtimeHub, session
}

func waitForPlaybackSessionMissing(t *testing.T, sessionMgr *playback.SessionManager, sessionID string) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, err := sessionMgr.GetSession(sessionID)
		if errors.Is(err, playback.ErrSessionNotFound) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	session, err := sessionMgr.GetSession(sessionID)
	if err == nil && session != nil {
		t.Fatalf("session %q still exists after fallback deadline", sessionID)
	}
	t.Fatalf("unexpected GetSession result after fallback deadline: %v", err)
}
