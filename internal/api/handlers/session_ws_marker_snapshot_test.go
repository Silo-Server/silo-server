package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestHandleSessionWebSocketSendsStoredMarkersAfterHello(t *testing.T) {
	for _, test := range []struct {
		name     string
		storage  string
		wantSent bool
	}{
		{name: "stored markers", storage: "", wantSent: true},
		// On-demand markers are never saved; the stored row would clear the
		// online markers the player already shows.
		{name: "on-demand storage", storage: string(markers.OnlineStorageOnDemand), wantSent: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionMgr := playback.NewSessionManager(0, 0)
			session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			introStart, introEnd := 12.0, 75.0
			handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: &models.MediaFile{ID: 100, IntroStart: &introStart, IntroEnd: &introEnd}})
			handler.RealtimeHub = playback.NewRealtimeHub()
			handler.MarkerUpdateNotifier = playback.NewMarkerUpdateNotifier(sessionMgr, handler.RealtimeHub)
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{markers.SettingOnlineStorage: test.storage}}

			router := chi.NewRouter()
			router.Get("/playback/ws/{session_id}", func(w http.ResponseWriter, r *http.Request) {
				ctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
				handler.HandleSessionWebSocket(w, r.WithContext(ctx))
			})
			server := httptest.NewServer(router)
			defer server.Close()
			conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/playback/ws/"+session.ID, nil)
			if err != nil {
				t.Fatalf("Dial websocket: %v", err)
			}
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			defer func() { _ = conn.Close() }()

			if err := conn.WriteJSON(playback.HelloEnvelope{
				Type:      playback.RealtimeMessageTypeHello,
				SessionID: session.ID,
				Client:    playback.HelloClientInfo{Name: "web", Version: "1.0.0"},
			}); err != nil {
				t.Fatalf("WriteJSON hello: %v", err)
			}

			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			_, data, err := conn.ReadMessage()
			if !test.wantSent {
				if err == nil {
					t.Fatalf("unexpected message after hello: %s", data)
				}
				return
			}
			if err != nil {
				t.Fatalf("read snapshot: %v", err)
			}
			var event playback.EventEnvelope
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			var payload playback.MarkersUpdatedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if event.Name != playback.RealtimeEventMarkersUpdated || payload.SessionID != session.ID || payload.FileID != 100 ||
				payload.Intro == nil || payload.Intro.Start != introStart || payload.Intro.End != introEnd {
				t.Fatalf("snapshot = %s", data)
			}
		})
	}
}
