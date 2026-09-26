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

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
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
				r.Header.Set("Origin", "null")
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
	sessions := &socketSessionFixture{valid: true}
	users := &socketUserFixture{user: models.User{ID: 7, Role: "user", Enabled: true}}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true, PolicyRevision: 1}}
	h.Validate = newSocketAuthorityValidator(sessions, users, viewer, func(context.Context, int, string) (bool, bool, error) { return false, true, nil })
	identity := evt.SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "profile", ProfileToken: "original-pin", AccessExpiresAt: time.Now().Add(time.Minute)}
	proof, _, err := h.Room.TokenService.Mint(watchtogether.RoomTokenClaims{RoomID: "room", UserID: 7, ProfileID: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{"other", ""} {
		if _, _, err = h.MintRoomSocket(t.Context(), room, proof, identity); err == nil {
			t.Fatal("foreign room proof minted")
		}
	}
	if _, _, err = h.MintRoomSocket(t.Context(), "room", proof, identity); err != nil {
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

// A browser on a LAN address or IP:port sends the address it loaded the page
// from, not the configured public URL. The room socket admits that same-host
// origin and still refuses a foreign one.
func TestRoomSocketV2AdmitsRequestOriginWithPublicOriginConfigured(t *testing.T) {
	h, _, _ := roomSocketHandler(t)
	server := roomSocketServer(t, h)
	h.SetPublicOrigin("https://public.example.test")
	ticket := roomSocketSeed(t, h, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, eventsTicketProtocolPrefix + ticket}}
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/rooms/room/ws"
	conn, resp, err := dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{"https://foreign.example.test"}})
	if conn != nil {
		_ = conn.Close()
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: want 403 refusal, got resp=%v err=%v", resp, err)
	}
	conn, resp, err = dialer.DialContext(t.Context(), endpoint, http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatalf("request origin refused with a public origin configured: %v", err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"snapshot"`) {
		t.Fatalf("snapshot %s %v", body, err)
	}
}
