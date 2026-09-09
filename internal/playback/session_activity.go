package playback

import (
	"context"
	"log/slog"
	"time"
)

// SessionActivityReader reads persisted activity for the supplied native
// sessions, validating their account/profile ownership. Missing entries carry
// no additional activity. A failed lookup must not authorize session expiry.
type SessionActivityReader func(context.Context, []Session) (map[string]time.Time, error)

func (m *SessionManager) SetCompatActivityReader(reader SessionActivityReader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compatActivityReader = reader
}

func (m *SessionManager) refreshCompatActivity(activeIdle, pausedIdle time.Duration) map[*Session]bool {
	m.mu.RLock()
	reader := m.compatActivityReader
	if reader == nil {
		m.mu.RUnlock()
		return nil
	}
	now := time.Now()
	var candidates []Session
	originals := make(map[string]*Session)
	for id, s := range m.sessions {
		active, paused := remoteTransportGrace(s, activeIdle, pausedIdle)
		if s.IsJellyfinCompat && s.activeTransportCount == 0 && m.sessionIsInactiveLocked(s, now, active, paused) {
			candidates = append(candidates, *s)
			originals[id] = s
		}
	}
	m.mu.RUnlock()
	if len(candidates) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	protected := make(map[*Session]bool)
	for offset := 0; offset < len(candidates); offset += 256 {
		batch := candidates[offset:min(offset+256, len(candidates))]
		activity, err := reader(ctx, batch)
		if err != nil {
			slog.Warn("could not refresh shared compatibility playback activity", "error", err, "sessions", len(batch))
		}
		m.mu.Lock()
		for _, candidate := range batch {
			current := m.sessions[candidate.ID]
			if current == nil || current != originals[candidate.ID] {
				continue
			}
			if err != nil {
				protected[current] = true
				continue
			}
			stamp := activity[candidate.ID]
			if stamp.After(current.LastActivityAt) {
				current.LastActivityAt = stamp
				if stamp.After(current.UpdatedAt) {
					current.UpdatedAt = stamp
				}
			}
		}
		m.mu.Unlock()
	}
	return protected
}

// SessionExpiryCandidate carries the exact inactivity threshold selected under
// the manager lock. The claimer must arbitrate retirement with durable pings.
type SessionExpiryCandidate struct {
	Session
	InactiveBefore time.Time
}

type SessionExpiryClaimer func(context.Context, []SessionExpiryCandidate) (map[string]bool, error)

func (m *SessionManager) SetCompatExpiryClaimer(claimer SessionExpiryClaimer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compatExpiryClaimer = claimer
}

// Called with m.mu held so a successful claim cannot race local activity or a
// new transport. A single batched round trip has a strict 250ms deadline.
func (m *SessionManager) claimCompatExpiryLocked(now time.Time, activeIdle, pausedIdle time.Duration, protected map[*Session]bool) map[string]bool {
	if m.compatExpiryClaimer == nil {
		return nil
	}
	var candidates []SessionExpiryCandidate
	for _, s := range m.sessions {
		active, paused := remoteTransportGrace(s, activeIdle, pausedIdle)
		if !s.IsJellyfinCompat || protected[s] || s.activeTransportCount > 0 || !m.sessionIsInactiveLocked(s, now, active, paused) {
			continue
		}
		if len(candidates) == 256 {
			break
		}
		grace := active
		if s.IsPaused {
			grace = paused
		}
		candidates = append(candidates, SessionExpiryCandidate{Session: *s, InactiveBefore: now.Add(-max(grace, 0))})
	}
	claimed := make(map[string]bool)
	if len(candidates) == 0 {
		return claimed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result, err := m.compatExpiryClaimer(ctx, candidates)
	if err != nil {
		slog.Warn("could not claim shared compatibility playback expiry", "error", err, "sessions", len(candidates))
		return claimed
	}
	return result
}
