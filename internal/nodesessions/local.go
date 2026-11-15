package nodesessions

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// LocalTracker keeps a transcode node's active sessions in memory. The node
// has no Redis: it sends Snapshot to the core in every report, and the core
// writes the records Tracker would have written (IngestNodeSessions), under
// the node's registered URL and name. The records it keeps therefore carry
// neither.
type LocalTracker struct {
	mu       sync.Mutex
	sessions map[string]SessionInfo
}

// NewLocalTracker returns an empty tracker.
func NewLocalTracker() *LocalTracker {
	return &LocalTracker{sessions: make(map[string]SessionInfo)}
}

// Track records an active session, replacing any record with its id.
func (tr *LocalTracker) Track(_ context.Context, info SessionInfo) {
	if info.SessionID == "" {
		return
	}
	tr.mu.Lock()
	tr.sessions[info.SessionID] = info
	tr.mu.Unlock()
}

// Remove drops a session.
func (tr *LocalTracker) Remove(_ context.Context, sessionID string) {
	tr.mu.Lock()
	delete(tr.sessions, sessionID)
	tr.mu.Unlock()
}

// Cleanup drops every session. The next report then tells the core to delete
// this node's records.
func (tr *LocalTracker) Cleanup(context.Context) {
	tr.mu.Lock()
	clear(tr.sessions)
	tr.mu.Unlock()
}

// Snapshot returns the active sessions, newest first (by start time, then
// session id), so a report that has to leave some out keeps the newest.
func (tr *LocalTracker) Snapshot() []SessionInfo {
	tr.mu.Lock()
	out := make([]SessionInfo, 0, len(tr.sessions))
	for _, info := range tr.sessions {
		out = append(out, info)
	}
	tr.mu.Unlock()
	slices.SortFunc(out, func(a, b SessionInfo) int {
		if c := startedAt(b).Compare(startedAt(a)); c != 0 {
			return c
		}
		return strings.Compare(a.SessionID, b.SessionID)
	})
	return out
}

// startedAt is when a record says its session started: the nanosecond field
// when it has one, otherwise the RFC 3339 string; zero when neither parses.
func startedAt(info SessionInfo) time.Time {
	if info.StartedAtUnixNano > 0 {
		return time.Unix(0, info.StartedAtUnixNano)
	}
	t, _ := time.Parse(time.RFC3339, info.StartedAt)
	return t
}
