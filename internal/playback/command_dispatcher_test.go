package playback

import (
	"sync"
	"testing"
)

type dispatchTestConn struct {
	mu       sync.Mutex
	messages []any
	// written, when set, receives a signal after each message.
	written chan struct{}
}

func (c *dispatchTestConn) WriteJSON(v any) error {
	c.mu.Lock()
	c.messages = append(c.messages, v)
	c.mu.Unlock()
	if c.written != nil {
		select {
		case c.written <- struct{}{}:
		default:
		}
	}
	return nil
}

// sent is the safe reader for tests where a command is dispatched from a
// background goroutine rather than inline.
func (c *dispatchTestConn) sent() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.messages...)
}

// startPlaybackTestSession starts a session and fails the test on error.
func startPlaybackTestSession(t *testing.T, sessions *SessionManager, userID int, profileID string, fileID int, method PlayMethod) *Session {
	t.Helper()
	session, err := sessions.StartSession(userID, profileID, fileID, method, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	return session
}

// listenRealtimeTestConn registers a recording realtime connection for the
// session on hub until the test ends.
func listenRealtimeTestConn(t *testing.T, hub *RealtimeHub, sessionID string) *dispatchTestConn {
	t.Helper()
	conn := &dispatchTestConn{}
	reg := hub.Register(sessionID, conn)
	t.Cleanup(func() { hub.Unregister(reg) })
	return conn
}

// connectRealtimeTestConn marks the session realtime-connected and listens on it.
func connectRealtimeTestConn(t *testing.T, sessions *SessionManager, hub *RealtimeHub, sessionID string) *dispatchTestConn {
	t.Helper()
	if err := sessions.SetRealtimeConnection(sessionID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}
	return listenRealtimeTestConn(t, hub, sessionID)
}
