package playback

import "errors"

// ErrStaleRealtimeRegistration means this connection no longer owns its lane.
var ErrStaleRealtimeRegistration = errors.New("stale realtime registration")

// WithCurrentRegistration admits bounded local bookkeeping for one connection.
// Replacement and unregister wait until the callback returns. This is a
// connection-generation fence, not authentication, playback authority, a runtime
// lease, or a durable acknowledgement. Callers must validate those separately.
//
// The callback must not perform network I/O or call hub methods for this session:
// its write lane is locked. Return an error before making an unauthorized effect;
// errors are propagated, but effects already performed are not rolled back.
func (h *RealtimeHub) WithCurrentRegistration(reg *RealtimeRegistration, apply func() error) error {
	if h == nil || reg == nil || reg.lane == nil || reg.sessionID == "" {
		return ErrStaleRealtimeRegistration
	}
	h.mu.RLock()
	lane := h.connections[reg.sessionID]
	h.mu.RUnlock()
	if lane == nil || lane != reg.lane {
		return ErrStaleRealtimeRegistration
	}
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.closed || lane.conn == nil || lane.generation != reg.generation {
		return ErrStaleRealtimeRegistration
	}
	if apply == nil {
		return errors.New("realtime registration callback required")
	}
	return apply()
}
