package playback

import (
	"errors"
	"sync"
	"time"
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
	// connectedAt is when the current connection took the lane. Replicas
	// compare it to decide which holds the newest connection.
	connectedAt time.Time
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

	// laneObserver, when set, learns that a session's lane was opened (or
	// taken over by a new connection) and that it closed. It runs after the
	// hub's locks are released and must not block.
	laneObserver func(sessionID string, open bool)
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
		lane = &sessionLane{conn: conn, generation: 1, connectedAt: time.Now()}
		h.connections[sessionID] = lane
		observer := h.laneObserver
		h.mu.Unlock()
		if h.onInitialRegister != nil {
			h.onInitialRegister()
		}
		if observer != nil {
			observer(sessionID, true)
		}
		return &RealtimeRegistration{sessionID: sessionID, lane: lane, generation: 1}
	}
	observer := h.laneObserver
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
	lane.connectedAt = time.Now()
	reg := &RealtimeRegistration{
		sessionID:  sessionID,
		lane:       lane,
		generation: lane.generation,
	}
	lane.mu.Unlock()
	if observer != nil {
		observer(sessionID, true)
	}

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
	observer := h.laneObserver
	h.mu.Unlock()
	lane.mu.Unlock()
	if observer != nil {
		observer(reg.sessionID, false)
	}

	return true
}

// HasLane reports whether this process holds an open realtime lane for the
// session.
func (h *RealtimeHub) HasLane(sessionID string) bool {
	_, open := h.LaneConnectedAt(sessionID)
	return open
}

// LaneConnectedAt reports when the session's open lane took its current
// connection; open is false when this process holds no open lane for it.
func (h *RealtimeHub) LaneConnectedAt(sessionID string) (connectedAt time.Time, open bool) {
	if h == nil || sessionID == "" {
		return time.Time{}, false
	}
	h.mu.RLock()
	lane := h.connections[sessionID]
	h.mu.RUnlock()
	if lane == nil {
		return time.Time{}, false
	}
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.closed || lane.conn == nil {
		return time.Time{}, false
	}
	return lane.connectedAt, true
}

// LaneSessionIDs lists the sessions with an open lane in this process.
func (h *RealtimeHub) LaneSessionIDs() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	ids := make([]string, 0, len(h.connections))
	for sessionID := range h.connections {
		ids = append(ids, sessionID)
	}
	h.mu.RUnlock()
	return ids
}

// SetLaneObserver installs the lane open/close observer (see laneObserver).
func (h *RealtimeHub) SetLaneObserver(observer func(sessionID string, open bool)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.laneObserver = observer
	h.mu.Unlock()
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
