package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/gorilla/websocket"
)

func socketTestHandler() (*EventsSocketV2, *atomic.Bool) {
	invalid := new(atomic.Bool)
	h := &EventsSocketV2{Events: NewEventsHandler(evt.NewHub("socket-fixture", nil), nil, nil, nil, nil, nil, nil), Tickets: evt.NewSocketTicketStore(nil), checkInterval: time.Millisecond * 10}
	h.Validate = func(ctx context.Context, proof evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		if invalid.Load() {
			return ctx, nil, evt.ErrSocketTicket
		}
		return ctx, &auth.Claims{UserID: proof.UserID, Role: proof.Role, SessionID: proof.SessionID}, nil
	}
	return h, invalid
}
func socketTestTicket(t *testing.T, h *EventsSocketV2, expires time.Time) string {
	t.Helper()
	ticket, err := h.Tickets.Mint(t.Context(), evt.SocketIdentity{UserID: 7, Role: "user", SessionID: "session", ProfileID: "profile", AccessFingerprint: "scope", AccessExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
func TestEventsSocketV2ProofOriginProtocolAndReplay(t *testing.T) {
	h, _ := socketTestHandler()
	server := httptest.NewServer(h)
	defer server.Close()
	h.PublicOrigin = server.URL
	ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "?channels=user_state"
	dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{"https://other.example.test"}})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || resp.StatusCode != 403 {
		t.Fatal("foreign origin accepted")
	}
	_ = resp.Body.Close()
	conn, resp, err = dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if conn.Subprotocol() != EventsSocketProtocol {
		t.Fatal("credential protocol echoed")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"hello"`) || strings.Contains(string(body), ticket) {
		t.Fatalf("hello: %s %v", body, err)
	}
	again, replay, err := dialer.DialContext(t.Context(), endpoint, nil)
	if again != nil {
		_ = again.Close()
	}
	if err == nil || replay.StatusCode != 401 {
		t.Fatal("ticket replay accepted")
	}
	_ = replay.Body.Close()
}
func TestEventsSocketV2ClosesOnAuthorityLoss(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "expired"}[expiry], func(t *testing.T) {
			h, invalid := socketTestHandler()
			server := httptest.NewServer(h)
			defer server.Close()
			until := time.Now().Add(time.Minute)
			if expiry {
				until = time.Now().Add(150 * time.Millisecond)
			}
			ticket := socketTestTicket(t, h, until)
			dialer := websocket.Dialer{Subprotocols: []string{EventsSocketProtocol, eventsTicketProtocolPrefix + ticket}}
			conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"?channels=user_state", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			defer func() { _ = resp.Body.Close() }()
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal("connection did not become live", err)
			}
			if !expiry {
				invalid.Store(true)
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			for {
				_, _, err := conn.ReadMessage()
				if err != nil {
					if !websocket.IsUnexpectedCloseError(err, websocket.CloseAbnormalClosure) && websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
						return
					}
					if strings.Contains(err.Error(), "timeout") {
						t.Fatal("authority loss did not close socket")
					}
					return
				}
			}
		})
	}
}

type socketSessionFixture struct{ valid bool }

func (s *socketSessionFixture) IsValid(context.Context, string) (bool, error) { return s.valid, nil }

type socketUserFixture struct{ user models.User }

func (s *socketUserFixture) GetByID(context.Context, int) (*models.User, error) { return &s.user, nil }

type socketViewerFixture struct {
	scope access.Scope
	err   error
}

func (s *socketViewerFixture) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	return s.scope, s.err
}

func TestEventsSocketV2ValidatesCurrentSessionAccountAndProfile(t *testing.T) {
	events, _ := socketTestHandler()
	sessions := &socketSessionFixture{valid: true}
	users := &socketUserFixture{user: models.User{ID: 7, Role: "admin", Enabled: true}}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "secondary", ProfileVerified: true, PolicyRevision: 1}}
	primaryFlag := false
	h := NewEventsSocketV2(events.Events, events.Tickets, sessions, users, viewer, func(context.Context, int, string) (bool, bool, error) { return primaryFlag, true, nil }, "")
	proof := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "admin", ProfileID: "secondary", AccessExpiresAt: time.Now().Add(time.Minute)}
	ticket, err := h.Mint(t.Context(), proof)
	if err != nil {
		t.Fatal(err)
	}
	proof, err = h.Tickets.Consume(t.Context(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := h.Validate(t.Context(), proof)
	if err != nil || claims.Role == "admin" {
		t.Fatal("secondary profile gained administrator channels")
	}
	primaryFlag = true
	if _, _, err := h.Validate(t.Context(), proof); err == nil {
		t.Fatal("changed administrator-profile authority accepted")
	}
	primaryFlag = false
	sessions.valid = false
	if _, _, err := h.Validate(t.Context(), proof); err == nil {
		t.Fatal("revoked session accepted")
	}
	sessions.valid = true
	users.user.Enabled = false
	if _, _, err := h.Validate(t.Context(), proof); err == nil {
		t.Fatal("disabled account accepted")
	}
	users.user.Enabled = true
	viewer.scope.PolicyRevision++
	if _, _, err := h.Validate(t.Context(), proof); err == nil {
		t.Fatal("changed viewer policy accepted")
	}
	viewer.scope.PolicyRevision--
	viewer.err = evt.ErrSocketTicket
	if _, _, err := h.Validate(t.Context(), proof); err == nil {
		t.Fatal("lost profile proof accepted")
	}
}

func TestEventsSocketV2MalformedHandshakeDoesNotConsumeProof(t *testing.T) {
	for _, kind := range []string{"protocol", "key", "version", "body", "query-token"} {
		t.Run(kind, func(t *testing.T) {
			h, _ := socketTestHandler()
			ticket := socketTestTicket(t, h, time.Now().Add(time.Minute))
			req := httptest.NewRequest(http.MethodGet, "http://example.test/events/ws", nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			req.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol+", "+eventsTicketProtocolPrefix+ticket)
			switch kind {
			case "protocol":
				req.Header.Set("Sec-WebSocket-Protocol", eventsTicketProtocolPrefix+ticket)
			case "key":
				req.Header.Set("Sec-WebSocket-Key", "bad")
			case "version":
				req.Header.Set("Sec-WebSocket-Version", "12")
			case "body":
				req.ContentLength = 1
			case "query-token":
				req.URL.RawQuery = "token=forbidden"
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 400 {
				t.Fatal("malformed handshake accepted", rec.Code)
			}
			if _, err := h.Tickets.Consume(t.Context(), ticket); err != nil {
				t.Fatal("malformed handshake consumed proof")
			}
		})
	}
}
