package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type roomSocketFakeTickets struct {
	mu         sync.Mutex
	credential watchtogether.RoomSocketCredential
	consumed   bool
	calls      int
}

func (f *roomSocketFakeTickets) Mint(_ context.Context, c watchtogether.RoomSocketCredential) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credential = c
	f.consumed = false
	return strings.Repeat("a", 43), nil
}
func (f *roomSocketFakeTickets) Consume(_ context.Context, _ string, room string) (watchtogether.RoomSocketCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.consumed || f.credential.RoomID != room {
		return watchtogether.RoomSocketCredential{}, watchtogether.ErrRoomSocketCredential
	}
	f.consumed = true
	return f.credential, nil
}

type roomSocketRepo struct {
	watchtogether.RoomStore
	closed atomic.Bool
}

func (r *roomSocketRepo) GetRoomByID(context.Context, string) (*watchtogether.Room, error) {
	if r.closed.Load() {
		return nil, watchtogether.ErrRoomClosed
	}
	return &watchtogether.Room{ID: "room", HostUserID: 99, HostProfileID: "host", Phase: watchtogether.RoomPhaseLobby, PlaybackState: watchtogether.RoomPlaybackStateIdle, SelectionMode: watchtogether.RoomSelectionModeHostPick, GuestControlPolicy: watchtogether.GuestControlPolicyHostOnly, Generation: 1, IsPaused: true, AnchorUpdatedAt: time.Now()}, nil
}
func roomSocketHandler(t *testing.T) (*WatchTogetherSocketV2, *atomic.Bool, *roomSocketRepo) {
	t.Helper()
	repo := new(roomSocketRepo)
	service := watchtogether.NewService(repo, nil, nil, nil, nil, nil)
	t.Cleanup(service.Close)
	invalid := new(atomic.Bool)
	h := &WatchTogetherSocketV2{Room: &WatchTogetherHandler{Service: service, TokenService: watchtogether.NewRoomTokenService("synthetic-secret", time.Minute)}, Tickets: new(roomSocketFakeTickets), checkInterval: 5 * time.Millisecond}
	h.Validate = func(ctx context.Context, c evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		if invalid.Load() || !c.AccessExpiresAt.After(time.Now()) {
			return ctx, nil, evt.ErrSocketTicket
		}
		return access.SetScope(ctx, access.Scope{UserID: c.UserID, ProfileID: c.ProfileID, ProfileVerified: true}), &auth.Claims{UserID: c.UserID, Role: c.Role, SessionID: c.SessionID}, nil
	}
	return h, invalid, repo
}
func roomSocketSeed(t *testing.T, h *WatchTogetherSocketV2, accessExpiry, roomExpiry time.Time) string {
	t.Helper()
	ticket, err := h.Tickets.Mint(t.Context(), watchtogether.RoomSocketCredential{RoomID: "room", Session: evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "profile", AccessFingerprint: "scope", AccessExpiresAt: accessExpiry}, RoomProofExpiresAt: roomExpiry})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
func roomSocketServer(t *testing.T, h *WatchTogetherSocketV2) *httptest.Server {
	t.Helper()
	mux := chi.NewRouter()
	mux.Get("/rooms/{room_id}/ws", h.ServeHTTP)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	h.PublicOrigin = server.URL
	return server
}
func TestRoomSocketV2AdmissionAndCallbacks(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	server := roomSocketServer(t, h)
	ticket := roomSocketSeed(t, h, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/rooms/room/ws"
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{"https://foreign.example.test"}})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || resp.StatusCode != 403 {
		t.Fatal("foreign origin admitted")
	}
	_ = resp.Body.Close()
	conn, resp, err = dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if conn.Subprotocol() != watchtogether.RoomSocketProtocol {
		t.Fatal("credential echoed")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"snapshot"`) || strings.Contains(string(body), ticket) {
		t.Fatalf("snapshot %s %v", body, err)
	}
	if err = conn.WriteJSON(map[string]string{"type": "ping", "client_sent_at": "synthetic-time"}); err != nil {
		t.Fatal(err)
	}
	for {
		_, body, err = conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err = json.Unmarshal(body, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "pong" {
			if frame["client_sent_at"] != "synthetic-time" {
				t.Fatal("changed ping callback")
			}
			break
		}
	}
	if err = conn.WriteJSON(map[string]any{"type": "lobby_ready", "ready": true}); err != nil {
		t.Fatal(err)
	}
	for {
		_, body, err = conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `"type":"snapshot"`) {
			if !strings.Contains(string(body), `"lobby_ready":true`) {
				t.Fatalf("lobby ready not reflected: %s", body)
			}
			break
		}
	}
	again, replay, err := dialer.DialContext(t.Context(), endpoint, nil)
	if again != nil {
		_ = again.Close()
	}
	if err == nil || replay.StatusCode != 401 {
		t.Fatal("replay admitted")
	}
	_ = replay.Body.Close()
}
func TestRoomSocketV2ClosesOnRevocationAndExpiry(t *testing.T) {
	for _, cause := range []string{"session", "room_closed", "access_expiry", "proof_expiry"} {
		t.Run(cause, func(t *testing.T) {
			h, invalid, repo := roomSocketHandler(t)
			server := roomSocketServer(t, h)
			a, r := time.Now().Add(time.Minute), time.Now().Add(time.Minute)
			if cause == "access_expiry" {
				a = time.Now().Add(150 * time.Millisecond)
			}
			if cause == "proof_expiry" {
				r = time.Now().Add(150 * time.Millisecond)
			}
			ticket := roomSocketSeed(t, h, a, r)
			dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, eventsTicketProtocolPrefix + ticket}}
			conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rooms/room/ws", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			defer func() { _ = resp.Body.Close() }()
			if _, _, err = conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			if cause == "session" {
				invalid.Store(true)
			}
			if cause == "room_closed" {
				repo.closed.Store(true)
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			for {
				_, _, err = conn.ReadMessage()
				if err != nil {
					if strings.Contains(err.Error(), "timeout") {
						t.Fatal("healthy socket survived lost authority")
					}
					break
				}
			}
		})
	}
}

func TestRoomSocketV2ReplacementArrivesBeforeClose(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	server := roomSocketServer(t, h)
	connect := func() *websocket.Conn {
		t.Helper()
		ticket := roomSocketSeed(t, h, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
		dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, eventsTicketProtocolPrefix + ticket}}
		conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rooms/room/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		return conn
	}
	readType := func(conn *websocket.Conn, want string) map[string]any {
		t.Helper()
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read %s: %v", want, err)
			}
			if frame["type"] == want {
				return frame
			}
		}
	}
	old := connect()
	readType(old, "snapshot")
	winner := connect()
	readType(winner, "snapshot")
	terminal := readType(old, "connection_replaced")
	if terminal["reason"] != "This profile joined the Watch Party on another device." {
		t.Fatalf("terminal reason: %+v", terminal)
	}
	if _, _, err := old.ReadMessage(); err == nil {
		t.Fatal("displaced socket remained open")
	}
	// A stale disconnect cannot take authority away from the winning socket.
	if err := winner.WriteJSON(map[string]any{"type": "lobby_ready", "ready": true}); err != nil {
		t.Fatal(err)
	}
	frame := readType(winner, "snapshot")
	encoded, err := json.Marshal(frame)
	if err != nil || !strings.Contains(string(encoded), `"lobby_ready":true`) {
		t.Fatalf("winner lost authority: %s %v", encoded, err)
	}
}
func TestRoomSocketV2MalformedBeforeConsume(t *testing.T) {
	for _, kind := range []string{"query", "body", "protocol", "key", "version", "origin", "duplicate_origin"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _ := roomSocketHandler(t)
			mux := chi.NewRouter()
			mux.Get("/rooms/{room_id}/ws", h.ServeHTTP)
			r := httptest.NewRequest("GET", "http://example.test/rooms/room/ws", nil)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			r.Header.Set("Sec-WebSocket-Protocol", watchtogether.RoomSocketProtocol+", silo.ticket."+strings.Repeat("a", 43))
			switch kind {
			case "query":
				r.URL.RawQuery = "room_token=old-proof"
			case "body":
				r.ContentLength = 1
			case "protocol":
				r.Header.Set("Sec-WebSocket-Protocol", EventsSocketProtocol)
			case "key":
				r.Header.Set("Sec-WebSocket-Key", "bad")
			case "version":
				r.Header.Set("Sec-WebSocket-Version", "12")
			case "origin":
				r.Header.Set("Origin", "https://foreign.test")
			case "duplicate_origin":
				r.Header["Origin"] = []string{"http://example.test", "http://example.test"}
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 400 && w.Code != 403 {
				t.Fatal(w.Code)
			}
			if h.Tickets.(*roomSocketFakeTickets).calls != 0 {
				t.Fatal("burned malformed credential")
			}
		})
	}
}
func TestRoomSocketV2MintValidatesRoomAndCurrentPIN(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	users := &socketUserFixture{user: models.User{ID: 7, Role: "user", Enabled: true}}
	sessions := &socketSessionFixture{valid: true, account: users}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true, PolicyRevision: 1}}
	h.Validate = newSocketAuthorityValidator(sessions, users, viewer, apimw.NewActingAdminDecider(func(context.Context, int, string) (bool, bool, error) { return false, true, nil }, nil))
	identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "profile", ProfileToken: "original-pin", AccessExpiresAt: time.Now().Add(time.Minute)}
	proof, _, err := h.Room.TokenService.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{"other", ""} {
		if _, _, err = h.MintRoomSocket(t.Context(), room, proof, identity, false); err == nil {
			t.Fatal("foreign room proof minted")
		}
	}
	if _, _, err = h.MintRoomSocket(t.Context(), "room", proof, identity, false); err != nil {
		t.Fatal(err)
	}
	delegated := h.Tickets.(*roomSocketFakeTickets).credential.Session
	if delegated.ProfileToken != "original-pin" || delegated.AccessFingerprint == "" {
		t.Fatal("capture missing")
	}
	sessions.valid = false
	if _, _, err = h.Validate(t.Context(), delegated); err == nil {
		t.Fatal("revoked session")
	}
	sessions.valid = true
	users.user.Enabled = false
	if _, _, err = h.Validate(t.Context(), delegated); err == nil {
		t.Fatal("disabled user")
	}
	users.user.Enabled = true
	viewer.scope.PolicyRevision++
	if _, _, err = h.Validate(t.Context(), delegated); err == nil {
		t.Fatal("changed policy")
	}
	viewer.scope.PolicyRevision--
	viewer.err = errors.New("PIN revoked")
	if _, _, err = h.Validate(t.Context(), delegated); err == nil {
		t.Fatal("lost PIN")
	}
}

// TestRoomSocketV2KeepsAnotherAppFromHosting: the host is the account and
// profile, so a session another app opened, acting as the host's profile,
// would connect as the host and steer the room for the person's own device.
// It is refused a credential for a room it would host, admitted as a member
// of another host's room (the credential remembers what it is), and once
// connected its transport requests are refused whatever the room's guest
// policy. Other sessions are unchanged.
func TestRoomSocketV2KeepsAnotherAppFromHosting(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	mint := func(userID int, profileID string, otherApp bool) error {
		t.Helper()
		proof, _, err := h.Room.TokenService.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: userID, ProfileID: profileID})
		if err != nil {
			t.Fatal(err)
		}
		identity := evt.SocketIdentity{UserID: userID, SessionID: "session", Role: "user", ProfileID: profileID, AccessExpiresAt: time.Now().Add(time.Minute)}
		_, _, err = h.MintRoomSocket(t.Context(), "room", proof, identity, otherApp)
		return err
	}
	var refused *APIError
	if err := mint(99, "host", true); !errors.As(err, &refused) || refused.Status != http.StatusForbidden || refused.Message != SessionNotAllowedDetail {
		t.Fatalf("another app as the host: err = %v", err)
	}
	if err := mint(99, "host", false); err != nil {
		t.Fatalf("the host's own session: err = %v", err)
	}
	if err := mint(7, "profile", true); err != nil || !h.Tickets.(*roomSocketFakeTickets).credential.OtherApp {
		t.Fatalf("another app as a member: err = %v, credential %+v", err, h.Tickets.(*roomSocketFakeTickets).credential)
	}
	play := []byte(`{"type":"transport_request","action":"play"}`)
	if err := h.Room.handleRoomClientMessage(t.Context(), nil, nil, 7, "profile", roomCaller{otherApp: true}, play); !errors.Is(err, errOtherAppTransport) {
		t.Fatalf("another app's transport request: err = %v", err)
	}
	if err := h.Room.handleRoomClientMessage(t.Context(), nil, nil, 7, "profile", roomCaller{}, play); errors.Is(err, errOtherAppTransport) {
		t.Fatal("an ordinary member's transport request was refused as another app's")
	}
}

// TestRoomSocketV2CarriesTheOtherAppMarkToTheConnection: the mark a mint
// records for a session another app opened must reach the connection loop,
// or its transport requests would be served. Through a real handshake, such
// a member's transport_request answers the other-app refusal; an ordinary
// member's gets the room's own answer.
func TestRoomSocketV2CarriesTheOtherAppMarkToTheConnection(t *testing.T) {
	for _, otherApp := range []bool{true, false} {
		h, _, _ := roomSocketHandler(t)
		server := roomSocketServer(t, h)
		proof, _, err := h.Room.TokenService.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "profile"})
		if err != nil {
			t.Fatal(err)
		}
		identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "profile", AccessExpiresAt: time.Now().Add(time.Minute)}
		ticket, _, err := h.MintRoomSocket(t.Context(), "room", proof, identity, otherApp)
		if err != nil {
			t.Fatal(err)
		}
		dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, eventsTicketProtocolPrefix + ticket}}
		conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rooms/room/ws", http.Header{"Origin": []string{server.URL}})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if err = conn.WriteJSON(map[string]any{"type": "transport_request", "action": "play"}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var message string
		for message == "" {
			_, body, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			var frame map[string]any
			if err = json.Unmarshal(body, &frame); err != nil {
				t.Fatal(err)
			}
			if frame["type"] == "error" {
				message, _ = frame["message"].(string)
			}
		}
		_ = conn.Close()
		if refused := message == errOtherAppTransport.Error(); refused != otherApp {
			t.Fatalf("another app %v: transport answered %q", otherApp, message)
		}
	}
}

// TestRoomSocketV2OtherAppSeatHeld: while a member's seat is held, a session
// another app opened for the same account and profile is refused with an
// error frame of code forbidden whose message says the seat is still held
// and to try again: the holder may be the app's own connection that dropped
// without closing, so it must not claim another device holds it. Catches the
// refusal going back to naming another device, or changing the code a
// client acts on.
func TestRoomSocketV2OtherAppSeatHeld(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	server := roomSocketServer(t, h)
	dial := func(otherApp bool) *websocket.Conn {
		t.Helper()
		proof, _, err := h.Room.TokenService.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "profile"})
		if err != nil {
			t.Fatal(err)
		}
		identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "profile", AccessExpiresAt: time.Now().Add(time.Minute)}
		ticket, _, err := h.MintRoomSocket(t.Context(), "room", proof, identity, otherApp)
		if err != nil {
			t.Fatal(err)
		}
		dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, eventsTicketProtocolPrefix + ticket}}
		conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/rooms/room/ws", http.Header{"Origin": []string{server.URL}})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	holder := dial(false)
	// The holder's first frame says it is connected, so its seat is held.
	_ = holder.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := holder.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	app := dial(true)
	_ = app.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, body, err := app.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]string
	if err := json.Unmarshal(body, &frame); err != nil {
		t.Fatal(err)
	}
	if frame["type"] != "error" || frame["code"] != "forbidden" || frame["message"] != "This profile's place in the room is still held by another connection; try again shortly" {
		t.Fatalf("frame = %s", body)
	}
}

// TestRoomSocketV2AnotherAppAttachesOnlyItsOwnStream: an attach frame names
// a playback session; one of the same account and profile is accepted from
// any member, so a session another app's key opened could attach the
// person's own device's stream as its member state. Such a connection
// attaches only a stream its own login session started (by the attempt row);
// another login session's stream, an unknown one, and any attach while the
// lookup is unwired are refused. Other connections are not asked. Catches the
// attach going back to account-and-profile only for such a session.
func TestRoomSocketV2AnotherAppAttachesOnlyItsOwnStream(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	started := map[string]string{"phone-stream": "login-phone", "app-stream": "login-app"}
	asked := 0
	h.Room.StreamLoginSession = func(_ context.Context, id string) (string, error) {
		asked++
		if s, ok := started[id]; ok {
			return s, nil
		}
		return "", errors.New("no attempt")
	}
	attach := func(caller roomCaller, id string) error {
		t.Helper()
		return h.Room.handleRoomClientMessage(t.Context(), nil, nil, 7, "profile", caller, []byte(`{"type":"attach_session","session_id":"`+id+`"}`))
	}
	app := roomCaller{otherApp: true, loginSessionID: "login-app"}
	for _, id := range []string{"phone-stream", "unknown-stream"} {
		if err := attach(app, id); !errors.Is(err, errOtherAppAttach) {
			t.Fatalf("another app attaching %s: err = %v", id, err)
		}
	}
	if err := attach(app, "app-stream"); errors.Is(err, errOtherAppAttach) {
		t.Fatal("another app refused its own stream")
	}
	before := asked
	if err := attach(roomCaller{loginSessionID: "login-phone"}, "phone-stream"); errors.Is(err, errOtherAppAttach) {
		t.Fatal("the person's own connection refused")
	}
	if asked != before {
		t.Fatal("the person's own connection was asked about its stream")
	}
	h.Room.StreamLoginSession = nil
	if err := attach(app, "app-stream"); !errors.Is(err, errOtherAppAttach) {
		t.Fatalf("unwired lookup: err = %v", err)
	}
}
