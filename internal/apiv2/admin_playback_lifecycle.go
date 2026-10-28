package apiv2

import "github.com/Silo-Server/silo-server/internal/api/handlers"

// AdminPlaybackLifecycleEvent is the data of a session.started,
// session.paused, session.resumed or session.stopped event on the admin
// sessions channel (docs/realtime-api.md).
type AdminPlaybackLifecycleEvent struct {
	SessionID string `json:"session_id"`
	// Seq orders a session's transitions: a higher number is newer.
	Seq int64 `json:"seq"`
	// At is the database clock when the transition was written; Seq, not
	// At, orders a session's transitions.
	At Instant `json:"at"`
	// State is the session's state after the transition: playing, paused
	// or stopped.
	State string `json:"state"`
	// EndReason is why a stopped session ended, as its history entry
	// records it.
	EndReason string `json:"end_reason,omitempty"`
	// Session is the session's live admin row, as listAdminPlaybackSessions
	// returns it, on a started, paused or resumed event; absent when no
	// server has a live row for it.
	Session *AdminPlaybackSession `json:"session,omitempty"`
	// History is the admin history entry the end recorded, as
	// listAdminPlaybackHistory returns it, on a stopped event.
	History *AdminPlaybackHistoryEntry `json:"history,omitempty"`
}

// PlaybackLifecycleEncoder renders lifecycle events in the native shapes.
type PlaybackLifecycleEncoder struct{}

func (PlaybackLifecycleEncoder) EncodePlaybackLifecycleEvent(view handlers.PlaybackLifecycleEventView) any {
	t := view.Transition
	event := AdminPlaybackLifecycleEvent{SessionID: t.SessionID, Seq: t.Seq, At: NewInstant(t.At), State: t.State, EndReason: string(t.EndReason)}
	if view.Session != nil {
		session := adminPlaybackSessionOf(*view.Session)
		event.Session = &session
	}
	if view.History != nil {
		history := adminPlaybackHistoryEntryOf(*view.History)
		event.History = &history
	}
	return event
}
