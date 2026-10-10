package handlers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// sessionRoleFixture answers ActiveSessionRole from values a test changes
// while the connection is open.
type sessionRoleFixture struct {
	role    atomic.Value
	revoked atomic.Bool
	checks  atomic.Int64
}

func (f *sessionRoleFixture) ActiveSessionRole(context.Context, string) (string, bool, error) {
	f.checks.Add(1)
	return f.role.Load().(string), !f.revoked.Load(), nil
}

// TestEventsWebSocketV1ClosesWhenSessionChanges covers the v1 socket, which
// picks its channels from the token's role at the handshake: once the account's
// role changes or the session ends, the connection closes so the client
// reconnects with a refreshed token.
func TestEventsWebSocketV1ClosesWhenSessionChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionRoleFixture)
	}{
		{"role changed", func(f *sessionRoleFixture) { f.role.Store("user") }},
		{"session revoked", func(f *sessionRoleFixture) { f.revoked.Store(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := new(sessionRoleFixture)
			sessions.role.Store("admin")
			handler := &EventsHandler{hub: evt.NewHub("test", &cache.NoopEventBus{}), sessionCheckInterval: 10 * time.Millisecond}
			handler.SetSessionRoles(sessions)
			conn, readFrame := eventsWSTestConnWithHandler(t, handler,
				&auth.Claims{UserID: 1, Role: "admin", SessionID: "session"}, "?channels=user_settings")
			readFrame("hello")
			readFrame("subscribed")

			// Read until the server ends the connection.
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}()

			// An unchanged session keeps the connection open across checks.
			deadline := time.Now().Add(2 * time.Second)
			for sessions.checks.Load() < 3 {
				if time.Now().After(deadline) {
					t.Fatal("session was not rechecked")
				}
				select {
				case <-ended:
					t.Fatal("connection ended while the session was unchanged")
				case <-time.After(time.Millisecond):
				}
			}
			select {
			case <-ended:
				t.Fatal("connection ended while the session was unchanged")
			default:
			}

			tc.change(sessions)
			select {
			case <-ended:
			case <-time.After(2 * time.Second):
				t.Fatal("connection stayed open after the session changed")
			}
		})
	}
}
