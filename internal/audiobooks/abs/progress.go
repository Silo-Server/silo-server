package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// ProgressStore is the narrow slice of user_watch_progress access the ABS
// handlers need. Implemented by ABSProgressStore in
// internal/audiobooks/abs_progress_store.go.
type ProgressStore interface {
	// GetProgress returns the progress row for (userID, profileID, contentID).
	// Returns (nil, nil) when no row exists (not an error).
	GetProgress(ctx context.Context, userID, profileID, contentID string) (*ProgressRow, error)
	// ListProgressForAudiobooks returns all progress rows for (userID, profileID)
	// that correspond to audiobooks (media_items.type = 'audiobook').
	// Capped at limit rows (most-recently-updated first).
	ListProgressForAudiobooks(ctx context.Context, userID, profileID string, limit int) ([]ProgressRow, error)
	// UpsertProgress writes a progress row. Fields not set in the body
	// (currentTime/duration/isFinished/progress) should be merged by the
	// caller before invoking this.
	UpsertProgress(ctx context.Context, row ProgressRow) error
	// UpdateProgressPosition updates only the position_seconds field for
	// (userID, profileID, contentID). Used by session sync to avoid overwriting
	// is_finished / progress_pct that the user set explicitly.
	UpdateProgressPosition(ctx context.Context, userID, profileID, contentID string, positionSeconds float64) error
	// SetHideFromContinue toggles the hide_from_continue flag on a
	// progress row. Idempotent — succeeds even when no row matches.
	SetHideFromContinue(ctx context.Context, userID, profileID, contentID string, hide bool) error
	// DeleteProgress removes the progress row for (userID, profileID, contentID).
	// Idempotent — succeeds even when no row matches. Used by the ABS
	// "Reset Progress" affordance: DELETE /api/me/progress/{libraryItemId}.
	DeleteProgress(ctx context.Context, userID, profileID, contentID string) error
}

// ABSPlaybackSessionStore tracks the active /abs/api/items/{id}/play sessions
// for per-session listening-time accounting (migration 143).
// Implemented by ABSPlaybackSessionStore in
// internal/audiobooks/abs_playback_session_store.go.
type ABSPlaybackSessionStore interface {
	// InsertPlaybackSession creates the session row at play-start.
	InsertPlaybackSession(ctx context.Context, sess ABSPlaybackSession) error
	// GetPlaybackSession fetches a session by its ULID. Returns ErrNotFound
	// when absent.
	GetPlaybackSession(ctx context.Context, id string) (ABSPlaybackSession, error)
	// SyncPlaybackSession updates position + accumulated listening time.
	SyncPlaybackSession(ctx context.Context, id string, currentPositionSeconds float64, timeListeningSeconds int) error
	// ClosePlaybackSession sets closed_at to now().
	ClosePlaybackSession(ctx context.Context, id string) error
	// CloseOpenSessionsForPrincipal closes all active sessions for a user
	// profile, used when logout revokes that profile's tokens.
	CloseOpenSessionsForPrincipal(ctx context.Context, userID, profileID string) error
	// AggregateStats returns aggregated listening stats for (user, profile).
	AggregateStats(ctx context.Context, userID, profileID string) (Stats, error)
	// ListClosedSessions returns paginated closed sessions for (user, profile)
	// ordered by started_at DESC. Returns (rows, totalRowCount, error).
	ListClosedSessions(ctx context.Context, userID, profileID string, limit, offset int) ([]ABSPlaybackSession, int, error)
}

// Stats is the aggregated /me/listening-stats response shape.
type Stats struct {
	TotalTime int       // seconds
	Items     int       // distinct content_ids listened to
	Days      []DayStat // recent days (most-recent first)
	DayOfWeek [7]int    // index 0 = Sunday
	Monthly   []MonthStat
}

type DayStat struct {
	Date    string
	Seconds int
}

type MonthStat struct {
	Month   string
	Seconds int
}

// ProgressRow is the in-memory representation of a user_watch_progress row
// as the ABS handlers use it. Intentionally narrow — only the fields the ABS
// wire format cares about.
type ProgressRow struct {
	UserID          string
	ProfileID       string
	ContentID       string
	CurrentSeconds  float64
	DurationSeconds float64
	ProgressPct     float64
	IsFinished      bool
	UpdatedAt       time.Time
}

// ABSPlaybackSession is the in-memory representation of an abs_playback_sessions row.
type ABSPlaybackSession struct {
	ID                     string
	UserID                 string
	ProfileID              string
	ContentID              string
	MediaFileID            *int
	TimeListeningSeconds   int
	CurrentPositionSeconds float64
	StartedAt              time.Time
	LastSyncAt             time.Time
	ClosedAt               *time.Time
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleGetMyProgress — GET /abs/api/me/progress
// Lists all progress rows for the caller that belong to audiobooks.
// The ABS mobile client reads this on startup to seed resume positions.
func (h *Handler) handleGetMyProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.ProgressStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mediaProgress": []any{}})
		return
	}
	rows, err := h.deps.ProgressStore.ListProgressForAudiobooks(r.Context(), a.UserID, a.ProfileID, 500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), p.ContentID, access)
		if err != nil || item == nil {
			continue
		}
		out = append(out, progressRowToABS(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mediaProgress": out})
}

// handleGetItemProgress — GET /abs/api/me/progress/{libraryItemId}
// Returns the progress row for one item. 404 when no progress exists.
func (h *Handler) handleGetItemProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contentID := chi.URLParam(r, "libraryItemId")
	if h.deps.ProgressStore == nil {
		http.Error(w, "progress not found", http.StatusNotFound)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, access)
	if err != nil || item == nil {
		http.Error(w, "progress not found", http.StatusNotFound)
		return
	}
	p, err := h.deps.ProgressStore.GetProgress(r.Context(), a.UserID, a.ProfileID, contentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.Error(w, "progress not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, progressRowToABS(*p))
}

// progressBody is the JSON body for POST/PATCH /api/me/progress/{libraryItemId}.
// All fields are optional — only present fields update the row (PATCH semantics).
//
// EbookProgress / EbookLocation are emitted by the ABS clients (AudioBooth's
// BooksService writes them on every page turn). silo's audiobook-first catalog
// doesn't yet persist ebook position; the fields are accepted-and-ignored so
// the client write succeeds and the user isn't shown a sync error. They will
// flow into a dedicated ebook progress column when the ebook scanner lands.
type progressBody struct {
	CurrentTime   *float64 `json:"currentTime"`
	Duration      *float64 `json:"duration"`
	IsFinished    *bool    `json:"isFinished"`
	Progress      *float64 `json:"progress"`
	EbookProgress *float64 `json:"ebookProgress"`
	EbookLocation *string  `json:"ebookLocation"`
}

// handleSetItemProgress — POST /abs/api/me/progress/{libraryItemId}
// UPSERTs the progress row. Merges body fields over any existing row so a
// partial body (only currentTime) doesn't reset duration/isFinished.
//
// This matches sub-plan 3's HandleReportAudiobookProgress in intent but uses
// the ABS wire format and calls ProgressStore directly so the two code paths
// don't need a shared helper — their thresholds/semantics differ enough that
// keeping them separate is cleaner.
func (h *Handler) handleSetItemProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contentID := chi.URLParam(r, "libraryItemId")
	var body progressBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if h.deps.ProgressStore == nil {
		http.Error(w, "progress store unavailable", http.StatusServiceUnavailable)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, access)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	// Read existing row to merge (PATCH semantics).
	var cur ProgressRow
	if existing, err := h.deps.ProgressStore.GetProgress(r.Context(), a.UserID, a.ProfileID, contentID); err == nil && existing != nil {
		cur = *existing
	}

	next := ProgressRow{
		UserID:          a.UserID,
		ProfileID:       a.ProfileID,
		ContentID:       contentID,
		CurrentSeconds:  cur.CurrentSeconds,
		DurationSeconds: cur.DurationSeconds,
		ProgressPct:     cur.ProgressPct,
		IsFinished:      cur.IsFinished,
		UpdatedAt:       time.Now(),
	}
	if body.CurrentTime != nil {
		next.CurrentSeconds = *body.CurrentTime
	}
	if body.Duration != nil {
		next.DurationSeconds = *body.Duration
	}
	if body.Progress != nil {
		next.ProgressPct = *body.Progress
	}
	if body.IsFinished != nil {
		next.IsFinished = *body.IsFinished
	}
	if err := h.deps.ProgressStore.UpsertProgress(r.Context(), next); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	updated, err := h.deps.ProgressStore.GetProgress(r.Context(), a.UserID, a.ProfileID, contentID)
	if err != nil || updated == nil {
		// Best-effort: return the in-memory merged row rather than failing.
		h.publish(a.UserID, "user_item_progress_updated", map[string]any{"data": progressRowToABS(next)})
		writeJSON(w, http.StatusOK, progressRowToABS(next))
		return
	}
	payload := progressRowToABS(*updated)
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{"data": payload})
	writeJSON(w, http.StatusOK, payload)
}

// syncPayload is the JSON body for PATCH /abs/api/session/{sid}/sync.
type syncPayload struct {
	CurrentTime float64 `json:"currentTime"`
	// Accept both spellings: real ABS clients send "timeListening"; an older
	// silo-plugin draft used "timeListened". UnmarshalJSON merges them.
	TimeListening float64 `json:"timeListening"`
	TimeListened  float64 `json:"timeListened"`
}

// timeDelta returns the accumulated listening time from whichever spelling
// the client used. Both fields are tried; non-zero wins.
func (p syncPayload) timeDelta() float64 {
	if p.TimeListening != 0 {
		return p.TimeListening
	}
	return p.TimeListened
}

// handleSessionSync — PATCH /abs/api/session/{sid}/sync
// Heartbeat endpoint the ABS mobile client calls every ~10 s during playback.
// Updates current position in user_watch_progress and accumulates
// time_listening_seconds in abs_playback_sessions.
//
// IDOR guard: the session must belong to the calling user (404 otherwise
// so session existence isn't leaked to other users).
//
// Uses UpdateProgressPosition (not UpsertProgress) to avoid overwriting
// is_finished / progress_pct that the user set explicitly — a sync tick
// that arrives after the user marks a book finished must not un-finish it.
func (h *Handler) handleSessionSync(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sid := chi.URLParam(r, "sid")
	var p syncPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		// No session store wired yet — accept the sync but return success
		// rather than blocking the player.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Ownership gate.
	sess, err := h.deps.PlaybackSessionStore.GetPlaybackSession(r.Context(), sid)
	if err != nil || !sameABSPrincipal(a, sess.UserID, sess.ProfileID) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), sess.ContentID, access)
	if err != nil || item == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Accumulate listening time and update position in abs_playback_sessions.
	if err := h.deps.PlaybackSessionStore.SyncPlaybackSession(
		r.Context(), sid, p.CurrentTime, int(p.timeDelta()),
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Update position in user_watch_progress. Must NOT be a full upsert —
	// see comment in handleSetItemProgress re: not overwriting is_finished.
	if h.deps.ProgressStore != nil {
		_ = h.deps.ProgressStore.UpdateProgressPosition(
			r.Context(), a.UserID, a.ProfileID, sess.ContentID, p.CurrentTime,
		)
	}

	// Realtime push to other connected clients.
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{
		"data": map[string]any{
			"libraryItemId": sess.ContentID,
			"currentTime":   p.CurrentTime,
			"sessionId":     sid,
		},
	})
	h.publish(a.UserID, "user_session_updated", map[string]any{
		"id":            sid,
		"libraryItemId": sess.ContentID,
		"currentTime":   p.CurrentTime,
		"timeListening": p.timeDelta(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSessionClose — POST /abs/api/session/{sid}/close
// Finalises a play session. Sets closed_at on the abs_playback_sessions row.
// Only the owning user may close their session (IDOR guard).
func (h *Handler) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sid := chi.URLParam(r, "sid")
	if h.deps.PlaybackSessionStore == nil {
		// No store wired — accept close gracefully.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Ownership gate.
	sess, err := h.deps.PlaybackSessionStore.GetPlaybackSession(r.Context(), sid)
	if err != nil || !sameABSPrincipal(a, sess.UserID, sess.ProfileID) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if err := h.deps.PlaybackSessionStore.ClosePlaybackSession(r.Context(), sid); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.publish(a.UserID, "user_session_closed", map[string]any{
		"id":            sid,
		"libraryItemId": sess.ContentID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Serialisation helpers
// ---------------------------------------------------------------------------

// progressRowToABS shapes a ProgressRow into the ABS /me/progress wire format.
// The `id` field uses the real-ABS convention of "<userID>-<libraryItemId>".
func progressRowToABS(p ProgressRow) map[string]any {
	lastMs := p.UpdatedAt.UnixMilli()
	out := map[string]any{
		"id":            p.UserID + "-" + p.ContentID,
		"libraryItemId": p.ContentID,
		"mediaItemId":   p.ContentID,
		"currentTime":   p.CurrentSeconds,
		"duration":      p.DurationSeconds,
		"isFinished":    p.IsFinished,
		"progress":      p.ProgressPct,
		"startedAt":     lastMs,
		"finishedAt":    nil,
		"lastUpdate":    lastMs,
	}
	if p.IsFinished {
		out["finishedAt"] = lastMs
	}
	return out
}
