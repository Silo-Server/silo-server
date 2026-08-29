package playback

import (
	"encoding/json"
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

func TestCommandDispatcherDispatchToSession(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

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

func TestCommandDispatcherDispatchToUser(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	sessionA, _ := sessions.StartSession(1, "profile-1", 100, PlayDirect, false)
	sessionB, _ := sessions.StartSession(1, "profile-1", 101, PlayDirect, false)

	hub := NewRealtimeHub()
	connA := &dispatchTestConn{}
	connB := &dispatchTestConn{}
	regA := hub.Register(sessionA.ID, connA)
	regB := hub.Register(sessionB.ID, connB)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)

	dispatcher := NewCommandDispatcher(sessions, hub, NewCommandTracker())
	results := dispatcher.DispatchToUser(1, func(session *Session) (CommandEnvelope, time.Duration, func(), error) {
		command, err := NewCommandEnvelope(session.ID, "cmd-"+session.ID, CommandDisplayMessage, json.RawMessage(`{"message":"hi"}`))
		return command, 0, nil, err
	})

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if len(connA.messages) != 1 || len(connB.messages) != 1 {
		t.Fatalf("expected both sessions to receive one command, got %d and %d", len(connA.messages), len(connB.messages))
	}
}

func TestCommandDispatcherDispatchToAll(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	sessionA, _ := sessions.StartSession(1, "profile-1", 100, PlayDirect, false)
	sessionB, _ := sessions.StartSession(2, "profile-2", 200, PlayDirect, false)

	hub := NewRealtimeHub()
	connA := &dispatchTestConn{}
	connB := &dispatchTestConn{}
	regA := hub.Register(sessionA.ID, connA)
	regB := hub.Register(sessionB.ID, connB)
	defer hub.Unregister(regA)
	defer hub.Unregister(regB)

	dispatcher := NewCommandDispatcher(sessions, hub, NewCommandTracker())
	results := dispatcher.DispatchToAll(func(session *Session) (CommandEnvelope, time.Duration, func(), error) {
		command, err := NewCommandEnvelope(session.ID, "cmd-"+session.ID, CommandPause, nil)
		return command, 0, nil, err
	})

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if len(connA.messages) != 1 || len(connB.messages) != 1 {
		t.Fatalf("expected both sessions to receive one command, got %d and %d", len(connA.messages), len(connB.messages))
	}
}

func TestCommandDispatcherDisconnectedTarget(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-1", 100, PlayDirect, false)

	dispatcher := NewCommandDispatcher(sessions, NewRealtimeHub(), NewCommandTracker())
	command, err := NewCommandEnvelope(session.ID, "cmd-1", CommandPause, nil)
	if err != nil {
		t.Fatalf("NewCommandEnvelope: %v", err)
	}

	result := dispatcher.DispatchToSession(command, 0, nil)
	if result.DispatchErr != ErrRealtimeConnectionNotFound {
		t.Fatalf("DispatchErr = %v, want %v", result.DispatchErr, ErrRealtimeConnectionNotFound)
	}
}

func TestCommandDispatcherMissingTarget(t *testing.T) {
	dispatcher := NewCommandDispatcher(NewSessionManager(0, 0), NewRealtimeHub(), NewCommandTracker())
	command, err := NewCommandEnvelope("missing", "cmd-1", CommandPause, nil)
	if err != nil {
		t.Fatalf("NewCommandEnvelope: %v", err)
	}

	result := dispatcher.DispatchToSession(command, 0, nil)
	if result.DispatchErr != ErrSessionNotFound {
		t.Fatalf("DispatchErr = %v, want %v", result.DispatchErr, ErrSessionNotFound)
	}
}
