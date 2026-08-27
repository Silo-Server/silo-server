package playback

import (
	"errors"
	"sync"
)

// ErrRealtimeConnectionNotFound is returned when a session has no active realtime connection.
var ErrRealtimeConnectionNotFound = errors.New("realtime connection not found")

// RealtimeConnection is the minimal send interface required by the hub.
// Implementations must ensure WriteJSON is bounded by an external deadline or
// cancellation policy; the hub serializes writes per session and assumes each
// write returns in finite time.
type RealtimeConnection interface {
	WriteJSON(v any) error
}

type sessionLane struct {
	conn       RealtimeConnection
	mu         sync.Mutex
	closed     bool
	generation uint64
}

// RealtimeRegistration is an opaque ownership token for a realtime connection.
type RealtimeRegistration struct {
	sessionID  string
	lane       *sessionLane
	generation uint64
}

// SessionID returns the playback session owned by this registration.
func (r *RealtimeRegistration) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// RealtimeHub stores one active realtime connection per playback session.
type RealtimeHub struct {
	mu                   sync.RWMutex
	connections          map[string]*sessionLane
	onInitialRegister    func()
	onRegisterLaneLookup func(sessionID string, lane *sessionLane)
}

// NewRealtimeHub creates an empty realtime hub.
func NewRealtimeHub() *RealtimeHub {
	return &RealtimeHub{
		connections: make(map[string]*sessionLane),
	}
}

// Register associates a realtime connection with a playback session.
// A later registration for the same session replaces the prior connection.
func (h *RealtimeHub) Register(sessionID string, conn RealtimeConnection) *RealtimeRegistration {
	if h == nil || sessionID == "" || conn == nil {
		return nil
	}

	h.mu.Lock()
	lane := h.connections[sessionID]
	if lane == nil {
		lane = &sessionLane{conn: conn, generation: 1}
		h.connections[sessionID] = lane
		h.mu.Unlock()
		if h.onInitialRegister != nil {
			h.onInitialRegister()
		}
		return &RealtimeRegistration{sessionID: sessionID, lane: lane, generation: 1}
	}
	h.mu.Unlock()

	if h.onRegisterLaneLookup != nil {
		h.onRegisterLaneLookup(sessionID, lane)
	}
	lane.mu.Lock()
	if lane.closed {
		lane.mu.Unlock()
		return nil
	}
	lane.conn = conn
	lane.closed = false
	lane.generation++
	reg := &RealtimeRegistration{
		sessionID:  sessionID,
		lane:       lane,
		generation: lane.generation,
	}
	lane.mu.Unlock()

	return reg
}

// Unregister removes the active realtime connection for the given registration
// token only if it still matches the currently registered connection.
func (h *RealtimeHub) Unregister(reg *RealtimeRegistration) bool {
	if h == nil || reg == nil || reg.sessionID == "" || reg.lane == nil {
		return false
	}

	h.mu.RLock()
	lane, ok := h.connections[reg.sessionID]
	h.mu.RUnlock()
	if !ok || lane == nil || lane != reg.lane {
		return false
	}

	lane.mu.Lock()
	if lane.generation != reg.generation || lane.closed {
		lane.mu.Unlock()
		return false
	}
	lane.closed = true
	lane.conn = nil
	lane.generation++
	nextGeneration := lane.generation
	h.mu.Lock()
	if current, ok := h.connections[reg.sessionID]; ok && current == lane && lane.closed && lane.conn == nil && lane.generation == nextGeneration {
		delete(h.connections, reg.sessionID)
	}
	h.mu.Unlock()
	lane.mu.Unlock()

	return true
}

// Send writes a message to the active connection for the given session.
func (h *RealtimeHub) Send(sessionID string, message any) error {
	if h == nil || sessionID == "" {
		return ErrRealtimeConnectionNotFound
	}

	h.mu.RLock()
	lane, ok := h.connections[sessionID]
	if !ok || lane == nil {
		h.mu.RUnlock()
		return ErrRealtimeConnectionNotFound
	}
	h.mu.RUnlock()

	lane.mu.Lock()
	if lane.closed || lane.conn == nil {
		lane.mu.Unlock()
		return ErrRealtimeConnectionNotFound
	}
	err := lane.conn.WriteJSON(message)
	lane.mu.Unlock()
	return err
}

// SendRegistered writes only when reg still owns the active connection. It is
// used for connection-specific work that must not spill into a replacement
// websocket after a reconnect.
func (h *RealtimeHub) SendRegistered(reg *RealtimeRegistration, message any) error {
	if h == nil || reg == nil || reg.sessionID == "" || reg.lane == nil {
		return ErrRealtimeConnectionNotFound
	}

	h.mu.RLock()
	lane, ok := h.connections[reg.sessionID]
	h.mu.RUnlock()
	if !ok || lane == nil || lane != reg.lane {
		return ErrRealtimeConnectionNotFound
	}

	lane.mu.Lock()
	if lane.closed || lane.conn == nil || lane.generation != reg.generation {
		lane.mu.Unlock()
		return ErrRealtimeConnectionNotFound
	}
	err := lane.conn.WriteJSON(message)
	lane.mu.Unlock()
	return err
}
