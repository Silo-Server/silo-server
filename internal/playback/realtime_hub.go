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
	conn             RealtimeConnection
	mu               sync.Mutex
	closed           bool
	generation       uint64
	registrationDone chan struct{}
}

// RealtimeRegistration is an opaque ownership token for a realtime connection.
type RealtimeRegistration struct {
	sessionID  string
	lane       *sessionLane
	generation uint64
	done       <-chan struct{}
}

// SessionID returns the playback session owned by this registration.
func (r *RealtimeRegistration) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// Done closes when this registration stops owning the active connection.
func (r *RealtimeRegistration) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
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
		done := make(chan struct{})
		lane = &sessionLane{conn: conn, generation: 1, registrationDone: done}
		h.connections[sessionID] = lane
		h.mu.Unlock()
		if h.onInitialRegister != nil {
			h.onInitialRegister()
		}
		return &RealtimeRegistration{sessionID: sessionID, lane: lane, generation: 1, done: done}
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
	if lane.registrationDone != nil {
		close(lane.registrationDone)
	}
	done := make(chan struct{})
	lane.conn = conn
	lane.closed = false
	lane.generation++
	lane.registrationDone = done
	reg := &RealtimeRegistration{
		sessionID:  sessionID,
		lane:       lane,
		generation: lane.generation,
		done:       done,
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
	if lane.registrationDone != nil {
		close(lane.registrationDone)
		lane.registrationDone = nil
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
	_, err := h.sendIf(sessionID, message, nil)
	return err
}

func (h *RealtimeHub) sendIf(sessionID string, message any, predicate func() bool) (bool, error) {
	if h == nil || sessionID == "" {
		return false, ErrRealtimeConnectionNotFound
	}

	h.mu.RLock()
	lane, ok := h.connections[sessionID]
	if !ok || lane == nil {
		h.mu.RUnlock()
		return false, ErrRealtimeConnectionNotFound
	}
	h.mu.RUnlock()

	lane.mu.Lock()
	if lane.closed || lane.conn == nil {
		lane.mu.Unlock()
		return false, ErrRealtimeConnectionNotFound
	}
	if predicate != nil && !predicate() {
		lane.mu.Unlock()
		return false, nil
	}
	err := lane.conn.WriteJSON(message)
	lane.mu.Unlock()
	return true, err
}

func (h *RealtimeHub) sendRegisteredIf(reg *RealtimeRegistration, message any, predicate func() bool) (bool, error) {
	if h == nil || reg == nil || reg.sessionID == "" || reg.lane == nil {
		return false, ErrRealtimeConnectionNotFound
	}
	lane := reg.lane
	lane.mu.Lock()
	if lane.closed || lane.conn == nil || lane.generation != reg.generation {
		lane.mu.Unlock()
		return false, ErrRealtimeConnectionNotFound
	}
	if predicate != nil && !predicate() {
		lane.mu.Unlock()
		return false, nil
	}
	err := lane.conn.WriteJSON(message)
	lane.mu.Unlock()
	return true, err
}

// HasConnection reports whether a session currently has a registered socket.
func (h *RealtimeHub) HasConnection(sessionID string) bool {
	if h == nil || sessionID == "" {
		return false
	}
	h.mu.RLock()
	lane := h.connections[sessionID]
	h.mu.RUnlock()
	if lane == nil {
		return false
	}
	lane.mu.Lock()
	active := !lane.closed && lane.conn != nil
	lane.mu.Unlock()
	return active
}

// HasRegistration reports whether reg still owns the active connection.
func (h *RealtimeHub) HasRegistration(reg *RealtimeRegistration) bool {
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
	active := !lane.closed && lane.conn != nil && lane.generation == reg.generation
	lane.mu.Unlock()
	return active
}
