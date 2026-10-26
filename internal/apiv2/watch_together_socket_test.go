package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
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
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{map[string]*auth.Claims{memberToken: claims}}, fakeSessions{map[string]string{"session": "user"}}, nil, nil)
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
