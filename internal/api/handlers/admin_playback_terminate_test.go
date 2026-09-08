package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestAdminTerminateStopsThenNotifies drives the v2 terminate against a live
// session: the session is stopped first, the client is notified only when a
// lane is open, and a repeat on the now-unknown session is 404.
func TestAdminTerminateStopsThenNotifies(t *testing.T) {
	for _, lane := range []string{"offline", "online"} {
		t.Run(lane, func(t *testing.T) {
			control, sessionMgr, hub, session := newAdminPlaybackControlTestHandler(t)

			var conn *adminPlaybackControlTestConn
			if lane == "online" {
				conn = &adminPlaybackControlTestConn{}
				registration := hub.Register(session.ID, conn)
				if registration == nil {
					t.Fatal("register lane")
				}
				t.Cleanup(func() { hub.Unregister(registration) })
			}

			view, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2, Reason: "policy"})
			if err != nil {
				t.Fatal(err)
			}
			if !view.AuthorityRevoked || view.AlreadyRevoked || view.DurableState != AdminTerminateDurableStopped {
				t.Fatalf("view = %+v", view)
			}
			if lane == "online" {
				if !view.ClientNotified || view.Delivery != AdminTerminateDeliveryDispatched || len(conn.messages) != 1 {
					t.Fatalf("online view = %+v messages=%d", view, len(conn.messages))
				}
				env, ok := conn.messages[0].(playback.CommandEnvelope)
				if !ok || env.Name != playback.CommandTerminate || env.CommandID != view.CommandID || env.Reason != "policy" || env.IssuedBy == nil || env.IssuedBy.Kind != "admin" {
					t.Fatalf("envelope = %#v", conn.messages[0])
				}
			} else if view.ClientNotified || view.Delivery != AdminTerminateDeliveryUnavailable {
				t.Fatalf("offline view = %+v", view)
			}
			if _, err := sessionMgr.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
				t.Fatalf("session survived terminate: %v", err)
			}

			// The session is gone: a repeat is unknown, exactly as the bridge
			// answers, and nothing is re-dispatched.
			if _, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: session.ID, ActorID: 2}); !errors.Is(err, playback.ErrSessionNotFound) {
				t.Fatalf("repeat err = %v", err)
			}
			if conn != nil && len(conn.messages) != 1 {
				t.Fatalf("repeat dispatched: %d messages", len(conn.messages))
			}
			waitForPlaybackSessionMissing(t, sessionMgr, session.ID)
		})
	}
}

// TestAdminTerminateErrors covers the unknown-session 404, the invalid actor,
// and the unwired handler.
func TestAdminTerminateErrors(t *testing.T) {
	control, _, _, session := newAdminPlaybackControlTestHandler(t)
	if _, err := control.Terminate(context.Background(), AdminTerminateInput{SessionID: "missing", ActorID: 2}); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := control.Terminate(context.Background(), AdminTerminateInput{SessionID: session.ID, ActorID: 0}); !errors.Is(err, ErrAdminPlaybackCommandInvalid) {
		t.Fatalf("no actor err = %v", err)
	}
	var unwired *AdminPlaybackControlHandler
	if _, err := unwired.Terminate(context.Background(), AdminTerminateInput{SessionID: session.ID, ActorID: 2}); !errors.Is(err, ErrAdminTerminateUnavailable) {
		t.Fatalf("unwired err = %v", err)
	}
}
