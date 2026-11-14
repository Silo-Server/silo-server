package wsconn

import (
	"bytes"
	"compress/flate"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// With permessage-deflate negotiated, a compressed message round-trips, and
// a message small enough on the wire to pass gorilla's read limit but larger
// once inflated is refused with a 1009 close instead of being buffered.
func TestReadMessageBoundsInflatedSize(t *testing.T) {
	const limit = 16 << 10
	bomb := make([]byte, 1<<20)
	var wire bytes.Buffer
	fw, _ := flate.NewWriter(&wire, 1) // gorilla's default compression level
	_, _ = fw.Write(bomb)
	_ = fw.Flush()
	if wire.Len() >= limit {
		t.Fatalf("test message compresses to %d bytes, not under the %d-byte wire limit", wire.Len(), limit)
	}

	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{EnableCompression: true}).Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.Close() }()
		conn.SetReadLimit(limit)
		for {
			messageType, data, err := ReadMessage(conn, limit)
			if err != nil {
				serverErr <- err
				return
			}
			if err := conn.WriteMessage(messageType, data); err != nil {
				serverErr <- err
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	client, resp, err := (&websocket.Dialer{EnableCompression: true}).Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_ = resp.Body.Close()
	if ext := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(ext, "permessage-deflate") {
		t.Fatalf("permessage-deflate not negotiated: %q", ext)
	}
	// A safety net for a broken server, not a wait: every read below is
	// answered at once.
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))

	report := bytes.Repeat([]byte(`{"type":"state_report","position_ms":125000},`), 200)
	if err := client.WriteMessage(websocket.TextMessage, report); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, echo, err := client.ReadMessage(); err != nil || !bytes.Equal(echo, report) {
		t.Fatalf("compressed round trip: %d bytes, %v", len(echo), err)
	}

	if err := client.WriteMessage(websocket.BinaryMessage, bomb); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err = client.ReadMessage()
	if closeErr := (*websocket.CloseError)(nil); !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseMessageTooBig {
		t.Fatalf("inflated message: client read %v, want a 1009 close", err)
	}
	if err := <-serverErr; !errors.Is(err, websocket.ErrReadLimit) {
		t.Fatalf("server read error = %v, want websocket.ErrReadLimit", err)
	}
}
