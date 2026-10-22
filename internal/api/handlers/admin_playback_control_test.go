package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandlePauseSession_RequiresHelloReadyControlConnection(t *testing.T) {
	control, sessionMgr, realtimeHub, session := newAdminPlaybackControlTestHandler(t)

	registration := realtimeHub.Register(session.ID, &adminPlaybackControlTestConn{})
	if registration == nil {
		t.Fatal("expected raw realtime registration")
	}
	defer realtimeHub.Unregister(registration)

	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/pause", strings.NewReader(`{"deadline_ms":10}`))
	req = withPlaybackRouteParam(req, "session_id", session.ID)

	rr := httptest.NewRecorder()
	control.HandlePauseSession(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error != "realtime_unavailable" {
		t.Fatalf("error code = %q, want realtime_unavailable", resp.Error)
	}

	time.Sleep(40 * time.Millisecond)

	if _, err := sessionMgr.GetSession(session.ID); err != nil {
		t.Fatalf("pause should not schedule a fallback stop, GetSession: %v", err)
	}
}

func TestHandleStopSession_KeepsFallbackWhenRealtimeUnavailable(t *testing.T) {
	control, sessionMgr, _, session := newAdminPlaybackControlTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+session.ID+"/stop", strings.NewReader(`{"deadline_ms":10}`))
	req = withPlaybackRouteParam(req, "session_id", session.ID)

	rr := httptest.NewRecorder()
	control.HandleStopSession(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackControlResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "fallback_scheduled" {
		t.Fatalf("status = %q, want fallback_scheduled", resp.Status)
	}

	waitForPlaybackSessionMissing(t, sessionMgr, session.ID)
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
