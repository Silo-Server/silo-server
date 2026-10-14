package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// expectMessageTooBigClose writes an oversize message and asserts the server
// closes the connection with 1009 instead of buffering it. The write may fail
// once the server has already torn the connection down; that is the same
// outcome, so only the follow-up read is checked.
func expectMessageTooBigClose(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	oversize := `{"type":"hello","pad":"` + strings.Repeat("x", wsMaxClientMessageBytes*2) + `"}`
	_ = conn.WriteMessage(websocket.TextMessage, []byte(oversize))
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			// Server-initiated frames (pings are control frames and do not
			// surface here) may precede the close; keep reading.
			continue
		}
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatal("server kept the connection open after an oversize message")
		}
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) {
			t.Fatalf("read error = %v, want a %d close", err, websocket.CloseMessageTooBig)
		}
		if closeErr.Code != websocket.CloseMessageTooBig {
			t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.CloseMessageTooBig)
		}
		return
	}
}

// paddedHello is a legitimate hello carrying an unusually long client version
// string, still well under the read limit.
func paddedHello(sessionID string, padding int) playback.HelloEnvelope {
	return playback.HelloEnvelope{
		Type:      playback.RealtimeMessageTypeHello,
		SessionID: sessionID,
		Client:    playback.HelloClientInfo{Name: "silo-test", Version: strings.Repeat("9", padding)},
	}
}

func TestHandleSessionWebSocketClosesOnOversizeMessage(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewPlaybackHandler(sessionMgr)
	handler.RealtimeHub = playback.NewRealtimeHub()

	router := chi.NewRouter()
	router.Get("/playback/ws/{session_id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
		handler.HandleSessionWebSocket(w, r.WithContext(ctx))
	})
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/playback/ws/" + session.ID
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial websocket: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	defer func() { _ = conn.Close() }()

	// A hello close to the limit is still accepted.
	if err := conn.WriteJSON(paddedHello(session.ID, wsMaxClientMessageBytes/2)); err != nil {
		t.Fatalf("WriteJSON hello: %v", err)
	}
	waitForPlaybackRealtimeState(t, sessionMgr, session.ID, true)

	expectMessageTooBigClose(t, conn)
	waitForPlaybackRealtimeState(t, sessionMgr, session.ID, false)
}

func TestControlSocketClosesOnOversizeMessage(t *testing.T) {
	f := newControlSocketFixture(t)
	conn, _, err := f.dial(t, f.mint(t, controlInstallation), nil) //nolint:bodyclose // dial registers t.Cleanup to close the response body
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(paddedHello(f.session.ID, wsMaxClientMessageBytes/2)); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		s, err := f.manager.GetSession(f.session.ID)
		return err == nil && s.HasRealtimeConnection
	}, "padded hello did not make the session control-ready")

	expectMessageTooBigClose(t, conn)
	waitForCondition(t, func() bool { return f.lane() == nil }, "control lane survived an oversize message")
	waitForCondition(t, func() bool {
		s, err := f.manager.GetSession(f.session.ID)
		return err == nil && !s.HasRealtimeConnection
	}, "session stayed control-ready after an oversize message")
}
