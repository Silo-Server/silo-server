package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type serverControlTestConn struct {
	messages []any
}

func (c *serverControlTestConn) WriteJSON(v any) error {
	c.messages = append(c.messages, v)
	return nil
}

func TestServerControlRestartRequestsShutdown(t *testing.T) {
	t.Parallel()

	called := 0
	restartStatus := NewServerRestartStatusTracker()
	handler := NewServerControlHandler(func(context.Context) error {
		called++
		return nil
	}, nil, restartStatus)

	req := httptest.NewRequest(http.MethodPost, "/admin/server/restart", nil)
	rec := httptest.NewRecorder()
	handler.HandleRestart(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Fatalf("restart calls = %d, want 1", called)
	}

	var resp serverRestartResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "restart_requested" {
		t.Fatalf("status = %q, want restart_requested", resp.Status)
	}
	if resp.NotifiedSessions != 0 {
		t.Fatalf("notified_sessions = %d, want 0", resp.NotifiedSessions)
	}
	if snapshot := restartStatus.Snapshot(); !snapshot.RestartRequested {
		t.Fatal("RestartRequested = false, want true")
	}
}

func TestServerControlRestartUnavailable(t *testing.T) {
	t.Parallel()

	handler := NewServerControlHandler(nil, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPost, "/admin/server/restart", nil)
	rec := httptest.NewRecorder()
	handler.HandleRestart(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestServerControlRestartAlreadyRequested(t *testing.T) {
	t.Parallel()

	handler := NewServerControlHandler(func(context.Context) error {
		return ErrServerRestartAlreadyRequested
	}, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPost, "/admin/server/restart", nil)
	rec := httptest.NewRecorder()
	handler.HandleRestart(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp serverRestartResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "already_requested" {
		t.Fatalf("status = %q, want already_requested", resp.Status)
	}
}

func TestServerControlRestartNotifiesPlaybackSessions(t *testing.T) {
	t.Parallel()

	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	realtimeHub := playback.NewRealtimeHub()
	conn := &serverControlTestConn{}
	registration := realtimeHub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected realtime registration")
	}
	defer realtimeHub.Unregister(registration)

	dispatcher := playback.NewCommandDispatcher(sessionMgr, realtimeHub, nil)
	handler := NewServerControlHandler(func(context.Context) error {
		return nil
	}, dispatcher, NewServerRestartStatusTracker())

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/server/restart",
		strings.NewReader(`{"reason":"maintenance","message":"Restarting for maintenance"}`),
	)
	rec := httptest.NewRecorder()
	handler.HandleRestart(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp serverRestartResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.NotifiedSessions != 1 {
		t.Fatalf("notified_sessions = %d, want 1", resp.NotifiedSessions)
	}
	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(conn.messages))
	}

	command, ok := conn.messages[0].(playback.CommandEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want playback.CommandEnvelope", conn.messages[0])
	}
	if command.Name != playback.CommandServerRestarting {
		t.Fatalf("command name = %q, want %q", command.Name, playback.CommandServerRestarting)
	}
	if command.Reason != "maintenance" {
		t.Fatalf("reason = %q, want maintenance", command.Reason)
	}

	var payload map[string]string
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["message"] != "Restarting for maintenance" {
		t.Fatalf("payload message = %q, want custom message", payload["message"])
	}
}
