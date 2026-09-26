package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type fakeRoomSocket struct {
	calls       int
	room, proof string
	identity    evt.SocketIdentity
}

func (f *fakeRoomSocket) MintRoomSocket(_ context.Context, room, proof string, identity evt.SocketIdentity) (string, time.Time, error) {
	f.calls++
	f.room = room
	f.proof = proof
	f.identity = identity
	return strings.Repeat("a", 43), time.Now().Add(30 * time.Second), nil
}
func (f *fakeRoomSocket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.room = chi.URLParam(r, "room_id")
	http.Error(w, "credential required", http.StatusUnauthorized)
}
func TestRoomSocketTicketAndRawRoute(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeRoomSocket)
	deps.WatchTogetherSocket = f
	claims := &auth.Claims{UserID: 1, Role: "user", SessionID: "session", TokenType: auth.TokenTypeAccess, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: claims}}, fakeSessions{map[string]bool{"session": true}}, nil, nil)
	h := NewHandler(deps)
	path := Prefix + "/watch-together/rooms/room/ws-ticket"
	headers := profileOwner()
	headers["X-Room-Token"] = "original-room-proof"
	r := do(t, h, "POST", path, "", headers)
	if r.Code != 200 || f.room != "room" || f.proof != "original-room-proof" || f.identity.SessionID != "session" || f.identity.ProfileID != "p-owner" || !strings.Contains(r.Body.String(), `"protocol":"silo.room.v2"`) || !strings.Contains(r.Body.String(), `"max_connection_seconds":300`) {
		t.Fatalf("%d %s %+v", r.Code, r.Body.String(), f)
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable credential")
	}
	before := f.calls
	r = do(t, h, "POST", path, "", bearer(memberToken))
	if r.Code != 422 || f.calls != before {
		t.Fatal("profileless delegation", r.Code)
	}
	claims.ExpiresAt = nil
	r = do(t, h, "POST", path, "", headers)
	if r.Code != 403 || f.calls != before {
		t.Fatal("unbounded delegation", r.Code)
	}
	r = do(t, h, "GET", Prefix+"/watch-together/rooms/target/ws", "", nil)
	if r.Code != 401 || f.room != "target" {
		t.Fatal("raw route lost room id", r.Code, f.room)
	}
	deps.WatchTogetherSocket = nil
	r = do(t, NewHandler(deps), "GET", Prefix+"/watch-together/rooms/target/ws", "", nil)
	if r.Code != 503 {
		t.Fatal("absent socket", r.Code)
	}
}

// The raw operation must preserve the actual upgrade writer and chi path value.
type roomSocketRouteStore struct{ watchtogether.RoomStore }

func (roomSocketRouteStore) GetRoomByID(context.Context, string) (*watchtogether.Room, error) {
	return &watchtogether.Room{ID: "room", HostUserID: 9, HostProfileID: "host", Phase: watchtogether.RoomPhaseLobby, PlaybackState: watchtogether.RoomPlaybackStateIdle, SelectionMode: watchtogether.RoomSelectionModeHostPick, GuestControlPolicy: watchtogether.GuestControlPolicyHostOnly, AnchorUpdatedAt: time.Now()}, nil
}

type roomSocketRouteTickets struct {
	credential watchtogether.RoomSocketCredential
}

func (f *roomSocketRouteTickets) Mint(context.Context, watchtogether.RoomSocketCredential) (string, error) {
	return "", nil
}
func (f *roomSocketRouteTickets) Consume(context.Context, string, string) (watchtogether.RoomSocketCredential, error) {
	return f.credential, nil
}
func TestRoomSocketUpgradeThroughV2Router(t *testing.T) {
	service := watchtogether.NewService(roomSocketRouteStore{}, nil, nil, nil, nil, nil)
	defer service.Close()
	credential := watchtogether.RoomSocketCredential{RoomID: "room", Session: evt.SocketIdentity{UserID: 1, ProfileID: "profile", SessionID: "session", Role: "user", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)}, RoomProofExpiresAt: time.Now().Add(time.Minute), ExpiresAt: time.Now().Add(30 * time.Second)}
	socket := &handlers.WatchTogetherSocketV2{Room: &handlers.WatchTogetherHandler{Service: service, TokenService: watchtogether.NewRoomTokenService("synthetic-route-secret", time.Minute)}, Tickets: &roomSocketRouteTickets{credential: credential}, Validate: func(ctx context.Context, c evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		return ctx, &auth.Claims{UserID: c.UserID, Role: c.Role, SessionID: c.SessionID}, nil
	}}
	deps := pilotDeps(nil, nil)
	deps.WatchTogetherSocket = socket
	server := httptest.NewServer(NewHandler(deps))
	defer server.Close()
	socket.PublicOrigin = server.URL
	dialer := websocket.Dialer{Subprotocols: []string{watchtogether.RoomSocketProtocol, "silo.ticket." + strings.Repeat("a", 43)}}
	conn, resp, err := dialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+Prefix+"/watch-together/rooms/room/ws", http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 101 || conn.Subprotocol() != watchtogether.RoomSocketProtocol {
		t.Fatal("raw handshake changed", resp.StatusCode)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(body), `"type":"snapshot"`) {
		t.Fatalf("%s %v", body, err)
	}
}
