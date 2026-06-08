package abssocket_test

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
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

// TestNew_BuildsAndExposesHandler is the bare-minimum smoke: New returns a
// Server, Handler is non-nil, Close is a no-op when never used.
func TestNew_BuildsAndExposesHandler(t *testing.T) {
	s := abssocket.New(
		func() []byte { return nil },
		nil, // nil tokenValidator → skip revocation check
		nil, // nil logger → noop
		nil, // default in-memory adapter
	)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.Handler() == nil {
		t.Fatal("Handler() returned nil")
	}
	s.Close()
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

// TestPublish_NoConnections_IsNoOp ensures the publish path doesn't panic
// when no sockets are connected to the user's room.
func TestPublish_NoConnections_IsNoOp(t *testing.T) {
	s := abssocket.New(
		func() []byte { return nil },
		nil,
		nil,
		nil,
	)
	defer s.Close()

	// Should not panic, should not block.
	s.Publish("u-no-such-user", "user_item_progress_updated", map[string]any{"x": 1})
	s.Publish("", "noop", nil) // empty user id is an explicit no-op
}

// TestEventPublisherInterfaceSatisfied is a compile-time check (executed at
// test time only). It guarantees that abssocket.Server stays usable wherever
// abs.EventPublisher is expected.
func TestEventPublisherInterfaceSatisfied(t *testing.T) {
	var _ abs.EventPublisher = (*abssocket.Server)(nil)
}
