package abs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/ebookformat"
	"github.com/Silo-Server/silo-server/internal/models"
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

// EbookProgressStore persists the reader state used by Silo's native ebook
// reader. It stays separate from ProgressStore: audiobooks use time-based
// user_watch_progress, while ebooks use CFI/location progress.
type EbookProgressStore interface {
	GetEbookProgress(ctx context.Context, userID, profileID, contentID string) (*EbookProgress, error)
	ListEbookProgress(ctx context.Context, userID, profileID string, limit int) ([]EbookProgress, error)
	// UpsertEbookProgress merges a partial write into the stored row and
	// returns the committed row. The merge happens inside the statement, so
	// callers must not read-modify-write. Returns ErrEbookProgressFileRequired
	// when no row exists yet and the patch carries no FileID.
	UpsertEbookProgress(ctx context.Context, patch EbookProgressPatch) (*EbookProgress, error)
	DeleteEbookProgress(ctx context.Context, userID, profileID, contentID string) error
	SetEbookHidden(ctx context.Context, userID, profileID, contentID string, hide bool) error
}

// ErrEbookProgressFileRequired reports that the first write for an item cannot
// be stored because no ebook file has been resolved yet. ebook_reader_progress
// holds a NOT NULL foreign key to media_files, so the caller has to pick the
// item's ebook file (respecting library access) and retry with FileID set.
var ErrEbookProgressFileRequired = errors.New("ebook progress: file id required for a new row")

type EbookProgress struct {
	UserID    string
	ProfileID string
	ContentID string
	FileID    int
	Location  string
	Progress  float64
	UpdatedAt time.Time
}

// EbookProgressPatch is a partial write against a reader-progress row. Nil
// fields are left to the stored value by the store's merge statement rather
// than being read into Go first: an autosave that only reports a location
// cannot blank the progress another device just wrote, and the hot path costs
// a single round trip.
type EbookProgressPatch struct {
	UserID    string
	ProfileID string
	ContentID string

	// FileID is only consulted when the row has to be inserted; an existing
	// row keeps its file unless a file is explicitly supplied.
	FileID   *int
	Location *string
	Progress *float64

	// AllowFinishedRegression lets progress fall back below
	// models.EbookFinishedProgressThreshold. Routine autosaves leave it false
	// so a late page-turn from a second device cannot un-finish a book.
	AllowFinishedRegression bool
	// ResetWhenFinished carries upstream Audiobookshelf's isFinished:false
	// semantics: clear progress, but only when the *stored* row was finished.
	// A row below the threshold keeps the progress it already had.
	ResetWhenFinished bool
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
// Lists all progress rows for the caller that belong to audiobooks and ebooks.
// The ABS mobile client reads this on startup to seed resume positions.
func (h *Handler) handleGetMyProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.ProgressStore == nil && h.deps.EbookProgressStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{mediaProgressKey: []any{}})
		return
	}
	var rows []ProgressRow
	var ebookRows []EbookProgress
	var err error
	if h.deps.ProgressStore != nil {
		rows, err = h.deps.ProgressStore.ListProgressForAudiobooks(r.Context(), a.UserID, a.ProfileID, 500)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if h.deps.EbookProgressStore != nil {
		ebookRows, err = h.deps.EbookProgressStore.ListEbookProgress(r.Context(), a.UserID, a.ProfileID, 500)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	ids := make([]string, 0, len(rows)+len(ebookRows))
	for _, p := range rows {
		ids = append(ids, p.ContentID)
	}
	for _, p := range ebookRows {
		ids = append(ids, p.ContentID)
	}
	// One batch fetch acts as the access/existence gate (the item itself isn't
	// rendered here), instead of one query per progress row.
	byID, err := h.deps.MediaStore.GetAudiobooksByIDs(r.Context(), ids, access)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows)+len(ebookRows))
	for _, p := range rows {
		if byID[p.ContentID] == nil {
			continue
		}
		out = append(out, progressRowToABS(p))
	}
	for _, p := range ebookRows {
		if byID[p.ContentID] != nil {
			out = append(out, ebookProgressToABS(p))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{mediaProgressKey: out})
}

// handleGetItemProgress — GET /abs/api/me/progress/{libraryItemId}
// Returns the progress row for one item. 404 when no progress exists.
func (h *Handler) handleGetItemProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	contentID := chi.URLParam(r, libraryItemIDKey)
	if h.deps.ProgressStore == nil && h.deps.EbookProgressStore == nil {
		http.Error(w, "progress not found", http.StatusNotFound)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, access)
	if err != nil || item == nil {
		http.Error(w, "progress not found", http.StatusNotFound)
		return
	}
	if item.Type == mediaTypeEbook {
		if h.deps.EbookProgressStore == nil {
			http.Error(w, "progress not found", http.StatusNotFound)
			return
		}
		p, err := h.deps.EbookProgressStore.GetEbookProgress(r.Context(), a.UserID, a.ProfileID, contentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p == nil {
			http.Error(w, "progress not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, ebookProgressToABS(*p))
		return
	}
	if h.deps.ProgressStore == nil {
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
// BooksService writes them on every page turn). They are persisted through
// Silo's native ebook_reader_progress store.
type progressBody struct {
	CurrentTime   *float64 `json:"currentTime"`
	Duration      *float64 `json:"duration"`
	IsFinished    *bool    `json:"isFinished"`
	Progress      *float64 `json:"progress"`
	EbookProgress *float64 `json:"ebookProgress"`
	EbookLocation *string  `json:"ebookLocation"`
}

const maxProgressBodyBytes int64 = 16 << 10

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
	contentID := chi.URLParam(r, libraryItemIDKey)
	r.Body = http.MaxBytesReader(w, r.Body, maxProgressBodyBytes)
	var body progressBody
	decoder := json.NewDecoder(r.Body)
	decodeErr := decoder.Decode(&body)
	if decodeErr == nil {
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			decodeErr = err
		}
	}
	if decodeErr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(decodeErr, &tooLarge) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if h.deps.ProgressStore == nil && h.deps.EbookProgressStore == nil {
		http.Error(w, "progress store unavailable", http.StatusServiceUnavailable)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, access)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}
	if item.Type == mediaTypeEbook {
		h.handleSetEbookProgress(w, r, a, contentID, body)
		return
	}
	if h.deps.ProgressStore == nil {
		http.Error(w, "progress store unavailable", http.StatusServiceUnavailable)
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
		h.publish(a.UserID, "user_item_progress_updated", map[string]any{dataKey: progressRowToABS(next)})
		writeJSON(w, http.StatusOK, progressRowToABS(next))
		return
	}
	payload := progressRowToABS(*updated)
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{dataKey: payload})
	writeJSON(w, http.StatusOK, payload)
}

// handleSetEbookProgress applies a partial reader-progress write.
//
// The body is translated into an EbookProgressPatch and handed to the store in
// one shot: merging with the stored row, the finished-regression guard, and
// upstream Audiobookshelf's isFinished:false rule are all evaluated against the
// stored row inside the write statement. Reading the row into Go first would
// let two devices reading the same book clobber each other between the read and
// the write.
func (h *Handler) handleSetEbookProgress(w http.ResponseWriter, r *http.Request, a ctxAuth, contentID string, body progressBody) {
	if h.deps.EbookProgressStore == nil {
		http.Error(w, "ebook progress unavailable", http.StatusServiceUnavailable)
		return
	}
	patch := EbookProgressPatch{UserID: a.UserID, ProfileID: a.ProfileID, ContentID: contentID}
	if body.EbookProgress != nil {
		if *body.EbookProgress < 0 || *body.EbookProgress > 1 {
			http.Error(w, "ebookProgress must be between 0 and 1", http.StatusBadRequest)
			return
		}
		progress := *body.EbookProgress
		patch.Progress = &progress
	}
	if body.EbookLocation != nil {
		location := *body.EbookLocation
		patch.Location = &location
	}
	if body.IsFinished != nil {
		if *body.IsFinished {
			finished := 1.0
			patch.Progress = &finished
		} else {
			// Un-finishing is explicit, so the regression guard steps aside.
			// Without an ebookProgress of its own the reset only applies to a
			// stored row that was actually finished; an in-progress row keeps
			// its place in the book.
			patch.AllowFinishedRegression = true
			patch.ResetWhenFinished = body.EbookProgress == nil
		}
	}

	committed, err := h.deps.EbookProgressStore.UpsertEbookProgress(r.Context(), patch)
	if errors.Is(err, ErrEbookProgressFileRequired) {
		fileID, ok := h.defaultEbookFileID(w, r, a, contentID)
		if !ok {
			return
		}
		patch.FileID = &fileID
		committed, err = h.deps.EbookProgressStore.UpsertEbookProgress(r.Context(), patch)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if committed == nil {
		http.Error(w, "ebook progress not committed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, ebookProgressToABS(*committed))
}

// defaultEbookFileID resolves the ebook file a brand-new progress row should
// point at: the configured primary when the item has one, otherwise the ABS
// default pick over the files this caller may see. It writes the HTTP error and
// returns false when the item has no readable ebook file.
func (h *Handler) defaultEbookFileID(w http.ResponseWriter, r *http.Request, a ctxAuth, contentID string) (int, bool) {
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return 0, false
	}
	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), contentID, access)
	if err != nil {
		http.Error(w, "load ebook files: "+err.Error(), http.StatusInternalServerError)
		return 0, false
	}
	selection, err := h.deps.MediaStore.GetPrimaryEbookFileID(r.Context(), contentID)
	if err != nil {
		http.Error(w, "load primary ebook: "+err.Error(), http.StatusInternalServerError)
		return 0, false
	}
	primary := ebookformat.PreferredFile(files)
	if selection.Configured {
		if !selection.HasPrimary {
			http.Error(w, "ebook not found", http.StatusNotFound)
			return 0, false
		}
		primary = ebookformat.FileByID(files, selection.FileID)
	}
	if primary == nil {
		http.Error(w, "ebook not found", http.StatusNotFound)
		return 0, false
	}
	return primary.ID, true
}

func ebookProgressToABS(p EbookProgress) map[string]any {
	lastUpdate := int64(0)
	if !p.UpdatedAt.IsZero() {
		lastUpdate = p.UpdatedAt.UnixMilli()
	}
	var finishedAt any
	if p.Progress >= models.EbookFinishedProgressThreshold {
		finishedAt = lastUpdate
	}
	return map[string]any{
		"id":             p.UserID + "-" + p.ContentID,
		userIDKey:        p.UserID,
		libraryItemIDKey: p.ContentID,
		episodeIDKey:     nil,
		durationKey:      0,
		progressKey:      p.Progress,
		currentTimeKey:   0,
		isFinishedKey:    p.Progress >= models.EbookFinishedProgressThreshold,
		"ebookLocation":  p.Location,
		"ebookProgress":  p.Progress,
		lastUpdateKey:    lastUpdate,
		"startedAt":      lastUpdate,
		"finishedAt":     finishedAt,
	}
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
		h.writeAccessResolutionError(w, r, err)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), sess.ContentID, access)
	if err != nil || item == nil || item.Type != mediaTypeAudiobook {
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
		if err := h.deps.ProgressStore.UpdateProgressPosition(
			r.Context(), a.UserID, a.ProfileID, sess.ContentID, p.CurrentTime,
		); err != nil {
			slog.WarnContext(r.Context(), "abs session sync: update progress position failed", "component", "audiobooks",
				"session_id", sid, "content_id", sess.ContentID, "error", err)
		}
	}
	h.updateNativePlaybackProgress(r.Context(), sid, p.CurrentTime)

	// Realtime push to other connected clients.
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{
		dataKey: map[string]any{
			libraryItemIDKey: sess.ContentID,
			currentTimeKey:   p.CurrentTime,
			"sessionId":      sid,
		},
	})
	h.publish(a.UserID, "user_session_updated", map[string]any{
		"id":             sid,
		libraryItemIDKey: sess.ContentID,
		currentTimeKey:   p.CurrentTime,
		timeListeningKey: p.timeDelta(),
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
	h.stopNativePlaybackSession(r.Context(), sid)

	h.publish(a.UserID, "user_session_closed", map[string]any{
		"id":             sid,
		libraryItemIDKey: sess.ContentID,
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
		"id":             p.UserID + "-" + p.ContentID,
		libraryItemIDKey: p.ContentID,
		"mediaItemId":    p.ContentID,
		currentTimeKey:   p.CurrentSeconds,
		durationKey:      p.DurationSeconds,
		isFinishedKey:    p.IsFinished,
		progressKey:      p.ProgressPct,
		startedAtKey:     lastMs,
		"finishedAt":     nil,
		lastUpdateKey:    lastMs,
	}
	if p.IsFinished {
		out["finishedAt"] = lastMs
	}
	return out
}
