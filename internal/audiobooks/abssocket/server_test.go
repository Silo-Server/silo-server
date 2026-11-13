package abssocket_test

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abssocket"
)

// recordingLogger captures Warn/Debug calls so we can assert on auth-reject
// paths without depending on hclog wiring.
type recordingLogger struct {
	mu   sync.Mutex
	logs []string
}

func (r *recordingLogger) Debug(msg string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, "debug:"+msg)
}

func (r *recordingLogger) Warn(msg string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, "warn:"+msg)
}

// TestHandler_ServesSocketIOOpenHandshake confirms the underlying engine
// returns the Engine.io handshake document on the initial polling GET. The
// payload starts with the Engine.io OPEN packet identifier ("0") followed
// by a JSON envelope carrying {sid, upgrades, pingInterval, pingTimeout}.
func TestHandler_ServesSocketIOOpenHandshake(t *testing.T) {
	s := abssocket.New(
		func() []byte { return []byte("a-32-byte-secret-for-handshakes!") },
		nil,
		&recordingLogger{},
		nil,
	)
	defer s.Close()

	req := httptest.NewRequest("GET", "/socket.io/?EIO=4&transport=polling", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("handshake status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if len(body) == 0 || body[0] != '0' {
		// Engine.io v4 prefixes the open packet with literal "0". Any
		// other shape means we're not actually serving Socket.io.
		t.Fatalf("body must start with Engine.io OPEN ('0'); got %q", body)
	}
}
