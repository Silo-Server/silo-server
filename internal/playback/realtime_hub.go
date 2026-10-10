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

// SendRegisteredIf writes a message only to the connection this registration
// identifies, and only while send still reports true. A replacement connection
// for the same session gets a new generation, so a write meant for an earlier
// connection is dropped rather than delivered to its successor. send runs under
// the per-session lane lock, so a later Send on the same session is ordered
// after this write.
func (h *RealtimeHub) SendRegisteredIf(reg *RealtimeRegistration, message any, send func() bool) (bool, error) {
	if h == nil || reg == nil || reg.lane == nil {
		return false, ErrRealtimeConnectionNotFound
	}
	lane := reg.lane
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.closed || lane.conn == nil || lane.generation != reg.generation {
		return false, ErrRealtimeConnectionNotFound
	}
	if send != nil && !send() {
		return false, nil
	}
	return true, lane.conn.WriteJSON(message)
}
