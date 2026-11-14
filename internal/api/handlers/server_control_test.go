package handlers

import (
	"context"
	"encoding/json"
	"errors"
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

// TestRequestAdminServerRestart covers the shared restart seam v2 calls:
// both accepted outcomes mark the restart tracker, a failed request does not,
// and connected playback sessions receive the restart notice first.
func TestRequestAdminServerRestart(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		restartErr    error
		wantStatus    string
		wantErr       bool
		wantRequested bool
	}{
		{name: "first request", wantStatus: "restart_requested", wantRequested: true},
		{name: "already requested", restartErr: ErrServerRestartAlreadyRequested, wantStatus: "already_requested", wantRequested: true},
		{name: "request fails", restartErr: errors.New("boom"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sessions := playback.NewSessionManager(0, 0)
			session, err := sessions.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			hub := playback.NewRealtimeHub()
			conn := &serverControlTestConn{}
			registration := hub.Register(session.ID, conn)
			if registration == nil {
				t.Fatal("expected realtime registration")
			}
			defer hub.Unregister(registration)

			calls := 0
			tracker := NewServerRestartStatusTracker()
			h := NewServerControlHandler(func(context.Context) error {
				calls++
				return tc.restartErr
			}, playback.NewCommandDispatcher(sessions, hub, nil), tracker)

			res, err := h.RequestAdminServerRestart(context.Background(), AdminServerRestartRequest{
				Reason:  "maintenance",
				Message: "Restarting for maintenance",
			})
			if calls != 1 {
				t.Fatalf("restart calls = %d, want 1", calls)
			}
			if got := tracker.Snapshot().RestartRequested; got != tc.wantRequested {
				t.Fatalf("RestartRequested = %v, want %v", got, tc.wantRequested)
			}
			if tc.wantErr {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != 500 {
					t.Fatalf("err = %v, want 500 APIError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequestAdminServerRestart: %v", err)
			}
			if res.Status != tc.wantStatus || res.NotifiedSessions != 1 {
				t.Fatalf("result = %+v, want status %q and 1 notified session", res, tc.wantStatus)
			}
			if len(conn.messages) != 1 {
				t.Fatalf("messages = %d, want 1", len(conn.messages))
			}
			command, ok := conn.messages[0].(playback.CommandEnvelope)
			if !ok {
				t.Fatalf("message type = %T, want playback.CommandEnvelope", conn.messages[0])
			}
			if command.Name != playback.CommandServerRestarting || command.Reason != "maintenance" {
				t.Fatalf("command = %q reason %q", command.Name, command.Reason)
			}
			var payload map[string]string
			if err := json.Unmarshal(command.Payload, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if payload["message"] != "Restarting for maintenance" || payload["title"] != "Server restarting" {
				t.Fatalf("payload = %v", payload)
			}
		})
	}
}
