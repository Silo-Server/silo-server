package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// sessionRoleFixture answers ActiveSession from values a test changes
// while the connection is open.
type sessionRoleFixture struct {
	role    atomic.Value
	revoked atomic.Bool
	handoff atomic.Bool
	checks  atomic.Int64
	// notActingAdmin flips the acting-admin decision, as when the household
	// gains a PIN under a profile-less connection; decisionDown makes the
	// decision fail, as when the profile store is unreachable.
	notActingAdmin atomic.Bool
	decisionDown   atomic.Bool
}

func (f *sessionRoleFixture) ActiveSession(context.Context, string) (auth.SessionAuthority, bool, error) {
	f.checks.Add(1)
	authority := auth.SessionAuthority{Role: f.role.Load().(string), Purpose: models.SessionPurposeAccount}
	if f.handoff.Load() {
		authority.Purpose, authority.BoundProfileID = models.SessionPurposeRemotePlayback, "profile"
	}
	return authority, !f.revoked.Load(), nil
}

// TestEventsWebSocketV1ClosesWhenSessionChanges covers the v1 socket, which
// picks its channels from the token's role at the handshake: once the account's
// role changes, the session ends, or the account stops acting as the admin, the
// connection closes so the client reconnects with a refreshed token.
func TestEventsWebSocketV1ClosesWhenSessionChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionRoleFixture)
	}{
		{"role changed", func(f *sessionRoleFixture) { f.role.Store("user") }},
		{"session revoked", func(f *sessionRoleFixture) { f.revoked.Store(true) }},
		// The role is left alone so only the handoff binding ends it.
		{"session bound as a remote-playback handoff", func(f *sessionRoleFixture) { f.handoff.Store(true) }},
		{"no longer acting admin", func(f *sessionRoleFixture) { f.notActingAdmin.Store(true) }},
		{"acting-admin decision fails", func(f *sessionRoleFixture) { f.decisionDown.Store(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := new(sessionRoleFixture)
			sessions.role.Store("admin")
			handler := &EventsHandler{hub: evt.NewHub("test", &cache.NoopEventBus{}), sessionCheckInterval: 10 * time.Millisecond}
			handler.SetSessionRoles(sessions)
			conn, readFrame := eventsWSTestConnActing(t, handler,
				&auth.Claims{UserID: 1, Role: "admin", SessionID: "session"}, "?channels=user_settings",
				func(context.Context, apimw.ActingAdminRequest) (bool, error) {
					if sessions.decisionDown.Load() {
						return false, errors.New("profile store down")
					}
					return !sessions.notActingAdmin.Load(), nil
				})
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

// profileProofFixture verifies a profile's PIN proof as the viewer resolver
// does: only the token the profile was unlocked with, until its PIN changes.
type profileProofFixture struct{ pinChanged atomic.Bool }

func (f *profileProofFixture) Resolve(_ context.Context, in access.ResolveInput) (access.Scope, error) {
	if f.pinChanged.Load() || in.ProfileToken != "pin-proof" {
		return access.Scope{}, access.ErrProfileUnverified
	}
	return access.Scope{UserID: in.UserID, ProfileID: in.ProfileID, ProfileVerified: true}, nil
}

// TestEventsWebSocketV1RechecksTicketProfileProof: a v1 connection holding
// the administrator channels as the primary profile its ticket was minted for
// re-verifies that profile's PIN proof, at the handshake and on every session
// check, so a PIN change closes it as on the v2 socket.
func TestEventsWebSocketV1RechecksTicketProfileProof(t *testing.T) {
	decide := func(_ context.Context, in apimw.ActingAdminRequest) (bool, error) {
		return in.ProfileID == "parent", nil
	}
	connect := func(t *testing.T, proofs *profileProofFixture, token string) (*websocket.Conn, func(string) map[string]json.RawMessage) {
		t.Helper()
		tickets := notifications.NewTicketStore(nil)
		ticket, _, err := tickets.Mint(t.Context(), notifications.Ticket{UserID: 1, ProfileID: "parent", ProfileToken: token})
		if err != nil {
			t.Fatal(err)
		}
		sessions := new(sessionRoleFixture)
		sessions.role.Store("admin")
		handler := &EventsHandler{hub: evt.NewHub("test", &cache.NoopEventBus{}), sessionCheckInterval: 10 * time.Millisecond}
		handler.SetNotificationsSystem(&notifications.System{Tickets: tickets})
		handler.SetSessionRoles(sessions)
		handler.SetProfileVerifier(proofs)
		return eventsWSTestConnActing(t, handler, &auth.Claims{UserID: 1, Role: "admin", SessionID: "session"},
			"?channels=user_settings&ticket="+url.QueryEscape(ticket), decide)
	}
	adminChannels := func(hello map[string]json.RawMessage) bool {
		return strings.Contains(string(hello["available_channels"]), `"sessions"`)
	}

	t.Run("stale proof at the handshake", func(t *testing.T) {
		_, readFrame := connect(t, new(profileProofFixture), "old-pin-proof")
		if adminChannels(readFrame("hello")) {
			t.Fatal("ticket with a stale PIN proof got the administrator channels")
		}
	})

	t.Run("PIN changes while connected", func(t *testing.T) {
		proofs := new(profileProofFixture)
		conn, readFrame := connect(t, proofs, "pin-proof")
		if !adminChannels(readFrame("hello")) {
			t.Fatal("verified primary profile did not get the administrator channels")
		}
		readFrame("subscribed")
		proofs.pinChanged.Store(true)
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				if strings.Contains(err.Error(), "timeout") {
					t.Fatal("connection stayed open after the PIN changed")
				}
				return
			}
		}
	})
}
