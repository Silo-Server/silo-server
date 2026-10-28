package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Playback session lifecycle: each session's announced state (playing,
// paused, stopped) lives in playback_session_lifecycle, and every transition
// takes the next number of one global sequence in the statement that makes
// it. The admin sessions channel carries each transition as a session.*
// event; delivery is at most once, and the list and history routes carry the
// same numbers so a monitor can order what it reads against what it heard.
// docs/architecture/playback-lifecycle.md describes the rules.

// Lifecycle states.
const (
	LifecyclePlaying = "playing"
	LifecyclePaused  = "paused"
	LifecycleStopped = "stopped"
)

// Lifecycle frontends: the surface a session started from. Only native
// sessions order their pause transitions by the client's progress sequence;
// the others take database arrival order.
const (
	LifecycleFrontendNative   = "native"
	LifecycleFrontendBridge   = "bridge"
	LifecycleFrontendJellyfin = "jellyfin"
	LifecycleFrontendABS      = "abs"
)

// Lifecycle event names on the sessions channel.
const (
	LifecycleEventStarted = "session.started"
	LifecycleEventPaused  = "session.paused"
	LifecycleEventResumed = "session.resumed"
	LifecycleEventStopped = "session.stopped"
)

// LifecycleTransition is one change of a session's announced state.
type LifecycleTransition struct {
	SessionID string
	Seq       int64
	At        time.Time
	State     string
	EndReason PlaybackEndReason
	// Started marks the transition that began the session, as opposed to a
	// resume, which also moves it to playing.
	Started bool
}

func (t LifecycleTransition) eventName() string {
	switch {
	case t.Started:
		return LifecycleEventStarted
	case t.State == LifecyclePaused:
		return LifecycleEventPaused
	case t.State == LifecycleStopped:
		return LifecycleEventStopped
	default:
		return LifecycleEventResumed
	}
}

// LifecycleStart is what a session's lifecycle row records when it starts.
type LifecycleStart struct {
	SessionID            string
	Frontend             string
	UserID               int
	ProfileID            string
	MediaFileID          int
	RequestedMediaFileID int
	PersistenceDisabled  bool
	StartedAt            time.Time
	// LiveRow is the session's playback_sessions_sync row as a sync would
	// write it now (worker.SessionSyncRowJSON).
	LiveRow json.RawMessage
}

// playbackLifecycleStore is the admin store's side of session lifecycle.
type playbackLifecycleStore interface {
	// StartLifecycle records a session's start. ok is false when the
	// session already has a row: it was announced, or it already ended.
	StartLifecycle(ctx context.Context, start LifecycleStart) (LifecycleTransition, bool, error)
	// SetLifecyclePaused moves a session that does not order its pauses by
	// a client sequence to paused or playing. ok is false when nothing
	// changed.
	SetLifecyclePaused(ctx context.Context, sessionID string, paused bool) (LifecycleTransition, bool, error)
	// RecordEnd ends the session's lifecycle and records its admin history
	// row in one transaction. The row takes the reason of the end that
	// stopped the lifecycle first, which may be an earlier one than entry's.
	// ok is true only for the end that moved an announced session to
	// stopped.
	RecordEnd(ctx context.Context, entry AdminPlaybackHistoryEntry, frontend string) (LifecycleTransition, bool, error)
}

func (s *PGPlaybackAdminStore) StartLifecycle(ctx context.Context, start LifecycleStart) (LifecycleTransition, bool, error) {
	if s == nil || s.pool == nil {
		return LifecycleTransition{}, false, nil
	}
	facts := start.LiveRow
	if len(facts) == 0 {
		facts = json.RawMessage("{}")
	}
	t := LifecycleTransition{SessionID: start.SessionID, State: LifecyclePlaying, Started: true}
	// The state starts at the attempt's latest accepted sample, read under a
	// share lock: progress accepted before the row exists is not lost, and
	// progress after it waits for the row and then updates it.
	err := s.pool.QueryRow(ctx, `
		WITH accepted AS (
			SELECT COALESCE((last_sample->>'is_paused')::boolean, FALSE) AS paused
			FROM playback_v3_attempts WHERE session_id = $1 FOR SHARE
		)
		INSERT INTO playback_session_lifecycle
			(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id,
			 progress_persistence_disabled, started_at, start_facts, updated_at)
		VALUES ($1, $2, CASE WHEN (SELECT paused FROM accepted) THEN 'paused' ELSE 'playing' END,
			nextval('playback_session_lifecycle_seq'), $3, $4, $5, $6, $7, $8, $9::jsonb, NOW())
		ON CONFLICT (session_id) DO NOTHING
		RETURNING seq, updated_at, state`,
		start.SessionID, start.Frontend, start.UserID, start.ProfileID, start.MediaFileID,
		start.RequestedMediaFileID, start.PersistenceDisabled, start.StartedAt, string(facts)).Scan(&t.Seq, &t.At, &t.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleTransition{}, false, nil
	}
	if err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback lifecycle start: %w", err)
	}
	return t, true, nil
}

func (s *PGPlaybackAdminStore) SetLifecyclePaused(ctx context.Context, sessionID string, paused bool) (LifecycleTransition, bool, error) {
	if s == nil || s.pool == nil {
		return LifecycleTransition{}, false, nil
	}
	t := LifecycleTransition{SessionID: sessionID, State: lifecycleState(paused)}
	err := s.pool.QueryRow(ctx, `
		UPDATE playback_session_lifecycle
		SET state = $2, seq = nextval('playback_session_lifecycle_seq'), updated_at = NOW()
		WHERE session_id = $1 AND frontend <> 'native' AND state NOT IN ('stopped', $2)
		RETURNING seq, updated_at`, sessionID, t.State).Scan(&t.Seq, &t.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleTransition{}, false, nil
	}
	if err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback lifecycle pause: %w", err)
	}
	return t, true, nil
}

func (s *PGPlaybackAdminStore) RecordEnd(ctx context.Context, entry AdminPlaybackHistoryEntry, frontend string) (LifecycleTransition, bool, error) {
	if s == nil || s.pool == nil {
		return LifecycleTransition{}, false, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback end: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	t := LifecycleTransition{SessionID: entry.SessionID, State: LifecycleStopped, EndReason: entry.EndReason}
	var inserted bool
	// A session with no row was never announced (it started before rows
	// existed, or its stop overtook its start): the end leaves a stopped
	// row, so a late start announces nothing, and publishes nothing itself.
	err = tx.QueryRow(ctx, `
		INSERT INTO playback_session_lifecycle
			(session_id, frontend, state, end_reason, seq, user_id, profile_id, media_file_id,
			 requested_media_file_id, started_at, updated_at)
		VALUES ($1, $2, 'stopped', $3, nextval('playback_session_lifecycle_seq'), $4, $5, $6, $6, $7::timestamptz, NOW())
		ON CONFLICT (session_id) DO UPDATE SET
			-- The number is taken under the row lock, not with the VALUES:
			-- a pause that commits while this waits must stay lower.
			state = 'stopped', end_reason = EXCLUDED.end_reason,
			seq = nextval('playback_session_lifecycle_seq'), updated_at = NOW()
		WHERE playback_session_lifecycle.state <> 'stopped'
		RETURNING seq, updated_at, xmax = 0`,
		entry.SessionID, frontend, string(entry.EndReason), entry.UserID, entry.ProfileID, entry.MediaFileID,
		entry.StartedAt).Scan(&t.Seq, &t.At, &inserted)
	won := err == nil && !inserted
	if errors.Is(err, pgx.ErrNoRows) {
		// Another end stopped the lifecycle first; the history row records
		// its reason.
		var reason string
		if err = tx.QueryRow(ctx, `SELECT seq, updated_at, end_reason FROM playback_session_lifecycle WHERE session_id = $1`,
			entry.SessionID).Scan(&t.Seq, &t.At, &reason); err == nil {
			t.EndReason = PlaybackEndReason(reason)
		}
	}
	if err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback lifecycle end: %w", err)
	}
	entry.EndReason = t.EndReason
	if err := tx.SendBatch(ctx, historyBatch(entry)).Close(); err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback history: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE admin_playback_history SET lifecycle_seq = $2
		WHERE session_id = $1 AND lifecycle_seq IS DISTINCT FROM $2`, entry.SessionID, t.Seq); err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback history sequence: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LifecycleTransition{}, false, fmt.Errorf("recording playback end: %w", err)
	}
	return t, won, nil
}

func lifecycleState(paused bool) string {
	if paused {
		return LifecyclePaused
	}
	return LifecyclePlaying
}

// PlaybackLifecycleEventView is a lifecycle transition with the views its
// event carries, before the native API renders it.
type PlaybackLifecycleEventView struct {
	Transition LifecycleTransition
	// Session is the session's live admin row; nil when no server has one.
	Session *AdminPlaybackSessionView
	// History is the admin history row an end recorded, as stored.
	History *AdminPlaybackHistoryRow
}

// PlaybackLifecycleEncoder renders a lifecycle event in the native API's
// shapes (apiv2 owns them).
type PlaybackLifecycleEncoder interface {
	EncodePlaybackLifecycleEvent(PlaybackLifecycleEventView) any
}

// PlaybackLifecycleViews reads the views a lifecycle event carries.
type PlaybackLifecycleViews interface {
	ReadAdminPlaybackSessions(ctx context.Context, query PlaybackSessionsQuery, after string, limit int) ([]AdminPlaybackSessionView, error)
	ReadAdminPlaybackHistoryEntry(ctx context.Context, sessionID string) (*AdminPlaybackHistoryRow, error)
}

// lifecycleEventsReady reports whether this server announces lifecycle
// transitions: a store for them, a hub to publish on and the native views.
func (h *PlaybackHandler) lifecycleEventsReady() (playbackLifecycleStore, bool) {
	if h == nil || h.EventsHub == nil || h.LifecycleEncoder == nil || h.LifecycleViews == nil {
		return nil, false
	}
	store, ok := h.AdminStore.(playbackLifecycleStore)
	return store, ok
}

// SessionStarted records a session's start and announces it. A session
// whose id already has a lifecycle row (a reconstruction, or one whose stop
// arrived first) announces nothing.
func (h *PlaybackHandler) SessionStarted(ctx context.Context, session *playback.Session, frontend string) {
	store, ok := h.lifecycleEventsReady()
	if !ok || session == nil || lifecycleIDParam(session.ID) == nil {
		return
	}
	ctx, cancel := lifecycleContext(ctx)
	defer cancel()
	var row json.RawMessage
	if h.LiveRowJSON != nil {
		var err error
		if row, err = h.LiveRowJSON(session); err != nil {
			slog.WarnContext(ctx, "failed to render a starting session's live row", "component", "api", "session", session.ID, "error", err)
		}
	}
	t, started, err := store.StartLifecycle(ctx, LifecycleStart{
		SessionID: session.ID, Frontend: frontend, UserID: session.UserID, ProfileID: session.ProfileID,
		MediaFileID: session.MediaFileID, RequestedMediaFileID: requestedMediaFileID(session),
		PersistenceDisabled: session.DisableProgressPersistence, StartedAt: session.StartedAt, LiveRow: row,
	})
	if err != nil {
		slog.WarnContext(ctx, "failed to record a session start", "component", "api", "session", session.ID, "error", err)
		return
	}
	if started {
		h.publishLifecycle(ctx, t, PlaybackSessionsQuery{Detached: row})
	}
}

// SessionPauseReported records a pause or resume a client reported without
// a progress sequence (Jellyfin, Audiobookshelf, the v1 bridge) and
// announces it when it changed the session's state.
func (h *PlaybackHandler) SessionPauseReported(ctx context.Context, sessionID string, paused bool) {
	store, ok := h.lifecycleEventsReady()
	if !ok || lifecycleIDParam(sessionID) == nil {
		return
	}
	ctx, cancel := lifecycleContext(ctx)
	defer cancel()
	t, changed, err := store.SetLifecyclePaused(ctx, sessionID, paused)
	if err != nil {
		slog.WarnContext(ctx, "failed to record a session pause", "component", "api", "session", sessionID, "error", err)
		return
	}
	if changed {
		h.publishLifecycle(ctx, t, PlaybackSessionsQuery{SessionID: sessionID})
	}
}

// publishSequencedPause announces a pause or resume a native client's
// accepted progress made (ApplyProgress commits it with the sample).
func (h *PlaybackHandler) publishSequencedPause(ctx context.Context, sessionID string, change *playback.LifecycleChangeV3) {
	if change == nil {
		return
	}
	if _, ok := h.lifecycleEventsReady(); !ok {
		return
	}
	t := LifecycleTransition{SessionID: sessionID, Seq: change.Seq, At: change.At, State: lifecycleState(change.Paused)}
	ctx, cancel := lifecycleContext(ctx)
	defer cancel()
	h.publishLifecycle(ctx, t, PlaybackSessionsQuery{SessionID: sessionID})
}

// lifecycleWriteTimeout bounds the lifecycle work a start or a progress
// report does inline, so a stalled database or event bus delays the request
// by at most this much.
const lifecycleWriteTimeout = 5 * time.Second

// lifecycleContext outlives the request (a client that disconnects right
// after a start or a pause still gets it recorded) but not the bound.
func lifecycleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), lifecycleWriteTimeout)
}

// recordEnd writes an end's admin history row through the lifecycle, and
// announces the end when it is the one that stopped an announced session.
// It reports false when this server keeps no lifecycle, so the caller
// writes the history row the old way.
func (h *PlaybackHandler) recordEnd(ctx context.Context, session *playback.Session, entry AdminPlaybackHistoryEntry) bool {
	store, ok := h.AdminStore.(playbackLifecycleStore)
	if !ok || lifecycleIDParam(session.ID) == nil {
		return false
	}
	t, won, err := store.RecordEnd(ctx, entry, sessionFrontend(session))
	if err != nil {
		slog.ErrorContext(ctx, "failed to record admin history", "component", "api", "session", session.ID, "error", err)
		return true
	}
	if !won {
		return true
	}
	if _, ready := h.lifecycleEventsReady(); !ready {
		return true
	}
	view := PlaybackLifecycleEventView{Transition: t}
	if history, err := h.LifecycleViews.ReadAdminPlaybackHistoryEntry(ctx, session.ID); err != nil {
		slog.WarnContext(ctx, "failed to read an ended session's history", "component", "api", "session", session.ID, "error", err)
	} else {
		view.History = history
	}
	h.publishLifecycleView(ctx, view)
	return true
}

// sessionFrontend names the surface a session started from, for the
// lifecycle row an end leaves when its start was never recorded.
func sessionFrontend(session *playback.Session) string {
	if session.IsJellyfinCompat {
		return LifecycleFrontendJellyfin
	}
	return LifecycleFrontendNative
}

func (h *PlaybackHandler) publishLifecycle(ctx context.Context, t LifecycleTransition, query PlaybackSessionsQuery) {
	view := PlaybackLifecycleEventView{Transition: t}
	sessions, err := h.LifecycleViews.ReadAdminPlaybackSessions(ctx, query, "", 1)
	switch {
	case err != nil:
		slog.WarnContext(ctx, "failed to read a session for its lifecycle event", "component", "api", "session", t.SessionID, "error", err)
	case len(sessions) > 0 && sessions[0].SessionID == t.SessionID:
		view.Session = &sessions[0]
		// The row's own number can trail this transition (the sync row is
		// older); the event says which transition it is.
		view.Session.LifecycleSeq = &t.Seq
		view.Session.IsPaused = t.State == LifecyclePaused
	}
	h.publishLifecycleView(ctx, view)
}

func (h *PlaybackHandler) publishLifecycleView(ctx context.Context, view PlaybackLifecycleEventView) {
	t := view.Transition
	event := t.eventName()
	if err := h.EventsHub.PublishJSON(ctx, evt.ChannelSessions, event, h.LifecycleEncoder.EncodePlaybackLifecycleEvent(view), evt.PublishOptions{
		AdminOnly: true,
		EventID:   "playback-lifecycle:" + strconv.FormatInt(t.Seq, 10),
	}); err != nil {
		slog.WarnContext(ctx, "failed to publish a playback lifecycle event", "component", "api", "session", t.SessionID, "event", event, "error", err)
	}
}
