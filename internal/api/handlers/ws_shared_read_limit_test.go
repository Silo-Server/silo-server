package handlers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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
