package handlers

import (
	"strings"
	"sync"
	"time"
)

// ServerRestartStatusTracker keeps process-local restart state. A real server
// restart creates a new tracker, which clears any pending restart banner state.
type ServerRestartStatusTracker struct {
	mu sync.RWMutex

	startedAt             time.Time
	restartRequired       bool
	restartRequiredAt     time.Time
	restartRequiredReason string
	restartRequested      bool
	restartRequestedAt    time.Time
}

type ServerRestartStatusSnapshot struct {
	StartedAt             time.Time
	RestartRequired       bool
	RestartRequiredAt     *time.Time
	RestartRequiredReason string
	RestartRequested      bool
	RestartRequestedAt    *time.Time
}

func NewServerRestartStatusTracker() *ServerRestartStatusTracker {
	return &ServerRestartStatusTracker{
		startedAt: time.Now().UTC(),
	}
}

func (s *ServerRestartStatusTracker) MarkRequired(reason string) {
	if s == nil {
		return
	}

	now := time.Now().UTC()
	reason = strings.TrimSpace(reason)

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.restartRequired {
		s.restartRequired = true
		s.restartRequiredAt = now
	}
	if reason != "" {
		s.restartRequiredReason = reason
	}
}

func (s *ServerRestartStatusTracker) MarkRestartRequested() {
	if s == nil {
		return
	}

	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.restartRequested {
		s.restartRequested = true
		s.restartRequestedAt = now
	}
}

func (s *ServerRestartStatusTracker) Snapshot() ServerRestartStatusSnapshot {
	if s == nil {
		now := time.Now().UTC()
		return ServerRestartStatusSnapshot{StartedAt: now}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var restartRequiredAt *time.Time
	if !s.restartRequiredAt.IsZero() {
		t := s.restartRequiredAt
		restartRequiredAt = &t
	}

	var restartRequestedAt *time.Time
	if !s.restartRequestedAt.IsZero() {
		t := s.restartRequestedAt
		restartRequestedAt = &t
	}

	return ServerRestartStatusSnapshot{
		StartedAt:             s.startedAt,
		RestartRequired:       s.restartRequired,
		RestartRequiredAt:     restartRequiredAt,
		RestartRequiredReason: s.restartRequiredReason,
		RestartRequested:      s.restartRequested,
		RestartRequestedAt:    restartRequestedAt,
	}
}
