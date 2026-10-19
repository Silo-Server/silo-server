package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// Exercise the actual bridge connection loops with synthetic authority and no
// media. A protocol read timeout is a failure, not evidence of disconnection.
func TestV1SocketsEndAtAccessExpiry(t *testing.T) {
	for _, surface := range []string{"events", "playback", "room"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			claims := &auth.Claims{UserID: 7, Role: "user", SessionID: "session", TokenType: auth.TokenTypeAccess}
			claims.ExpiresAt = &jwt.NumericDate{Time: time.Now().Add(300 * time.Millisecond)}
			mux := chi.NewRouter()
			mux.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r.WithContext(apimw.SetClaims(r.Context(), claims)))
				})
			})
			path := "/socket"
			switch surface {
			case "events":
				h := &EventsHandler{hub: evt.NewHub("expiry", &cache.NoopEventBus{})}
				mux.Get(path, h.HandleWebSocket)
				path += "?channels=user_settings"
			case "playback":
				manager := playback.NewSessionManager(0, 0)
				session, err := manager.StartSession(7, "profile", 1, playback.PlayDirect, false)
				if err != nil {
					t.Fatal(err)
				}
				h := NewPlaybackHandler(manager)
				h.RealtimeHub = playback.NewRealtimeHub()
				mux.Get("/socket/{session_id}", h.HandleSessionWebSocket)
				path += "/" + session.ID
			case "room":
				service := watchtogether.NewService(new(roomSocketRepo), nil, nil, nil, nil, nil)
				t.Cleanup(service.Close)
				h := &WatchTogetherHandler{Service: service, ScopeResolver: &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true}}}
				mux.Get("/socket/{room_id}", h.HandleRoomWebSocket)
				path += "/room?profile_id=profile"
			}
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			conn, resp, err := websocket.DefaultDialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close(); _ = resp.Body.Close() })
			assertSocketEnds(t, conn)
		})
	}
}

func assertSocketEnds(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				t.Fatal("socket survived authority expiry or revocation")
			}
			return
		}
	}
}
