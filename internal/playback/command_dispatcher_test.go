package playback

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type dispatchTestConn struct {
	mu       sync.Mutex
	messages []any
}

func (c *dispatchTestConn) WriteJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, v)
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

func TestCommandDispatcherDispatchToSession(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayDirect)
	hub := NewRealtimeHub()
	conn := listenRealtimeTestConn(t, hub, session.ID)

	tracker := NewCommandTracker()
	defer tracker.Close()
	dispatcher := NewCommandDispatcher(sessions, hub, tracker)

	command, err := NewCommandEnvelope(session.ID, "cmd-1", CommandPause, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("NewCommandEnvelope: %v", err)
	}

	result := dispatcher.DispatchToSession(command, time.Second, nil)
	if result.DispatchErr != nil {
		t.Fatalf("DispatchToSession: %v", result.DispatchErr)
	}
	if !result.Delivered {
		t.Fatal("expected delivered result")
	}
	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(conn.messages))
	}
}

func TestCommandDispatcherDisconnectedTarget(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session := startPlaybackTestSession(t, sessions, 1, "profile-1", 100, PlayDirect)

	dispatcher := NewCommandDispatcher(sessions, NewRealtimeHub(), NewCommandTracker())
	command, err := NewCommandEnvelope(session.ID, "cmd-1", CommandPause, nil)
	if err != nil {
		t.Fatalf("NewCommandEnvelope: %v", err)
	}

	result := dispatcher.DispatchToSession(command, 0, nil)
	if !errors.Is(result.DispatchErr, ErrRealtimeConnectionNotFound) {
		t.Fatalf("DispatchErr = %v, want %v", result.DispatchErr, ErrRealtimeConnectionNotFound)
	}
}
