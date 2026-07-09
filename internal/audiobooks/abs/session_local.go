package abs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// ---------------------------------------------------------------------------
// Offline session sync (ABS SessionController.syncLocal / syncLocalSessions)
// ---------------------------------------------------------------------------
//
// The official ABS mobile app records playback while offline into local
// PlaybackSession objects, then POSTs them back on reconnect so the server's
// media progress catches up. Two endpoints implement this:
//
//   POST /session/local      — one session   (SessionController.syncLocal)
//   POST /session/local-all  — many sessions (SessionController.syncLocalSessions)
//
// Real ABS (server/managers/PlaybackSessionManager.js):
//   - syncLocalSessionRequest: syncLocalSession(one) → 200 on success,
//     500 + error text on failure.
//   - syncLocalSessionsRequest: reads req.body.sessions, loops each through
//     syncLocalSession, replies { results: [ {id, success, error?,
//     progressSynced} ] } with HTTP 200 regardless of per-session outcome.
//   - syncLocalSession returns {id, success:false, error} when the library
//     item can't be found, else {id, success:true, progressSynced}.
//
// silo does not persist arbitrary client-supplied sessions, so we do not
// create a server-side playback-session row here. We mirror the *effect* that
// matters — the caller's resume position — into user_watch_progress and emit
// the same user_item_progress_updated realtime event handleSessionSync does.
// An existing progress row is advanced monotonically (UpdateProgressPosition);
// a book listened to entirely offline has no row yet, so one is created
// (UpsertProgress) rather than silently dropping the position. Accumulated
// offline listening time is not persisted: there is no store method to add
// standalone listening time without an existing session row. Position sync is
// the client-visible behaviour offline sync exists to restore.

// localPlaybackSession is the subset of the ABS PlaybackSession payload the
// client POSTs for offline sync that silo acts on. EpisodeID is a pointer so a
// present-but-null value (audiobook) is distinguishable from a podcast episode.
type localPlaybackSession struct {
	ID            string  `json:"id"`
	LibraryItemID string  `json:"libraryItemId"`
	EpisodeID     *string `json:"episodeId"`
	CurrentTime   float64 `json:"currentTime"`
	TimeListening float64 `json:"timeListening"`
	DisplayTitle  string  `json:"displayTitle"`
}

// localSyncResult mirrors the per-session object ABS returns from
// syncLocalSession: {id, success, error?, progressSynced}.
type localSyncResult struct {
	ID             string `json:"id"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
	ProgressSynced bool   `json:"progressSynced"`
}

// handleSyncLocalSession — POST /session/local
// Syncs a single offline-recorded session. Matches ABS syncLocalSessionRequest:
// 200 on success, 500 + error text when the item can't be resolved.
func (h *Handler) handleSyncLocalSession(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var sess localPlaybackSession
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&sess); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	res := h.syncOneLocalSession(r.Context(), a, access, sess)
	if !res.Success {
		// Real ABS: res.status(500).send(result.error).
		http.Error(w, res.Error, http.StatusInternalServerError)
		return
	}
	// Real ABS: res.sendStatus(200) — plain 200, no JSON body.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// handleSyncLocalSessions — POST /session/local-all
// Batch-syncs offline-recorded sessions. Matches ABS syncLocalSessionsRequest:
// reads {sessions: [...]}, loops each, always replies 200 with
// {results: [...]}. A bad session never fails the whole batch.
func (h *Handler) handleSyncLocalSessions(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Decode into raw messages so one malformed session doesn't sink the batch.
	var body struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	results := make([]localSyncResult, 0, len(body.Sessions))
	for _, raw := range body.Sessions {
		var sess localPlaybackSession
		if err := json.Unmarshal(raw, &sess); err != nil {
			slog.WarnContext(r.Context(), "abs local session sync: skipping malformed session", "component", "audiobooks", "error", err)
			results = append(results, localSyncResult{Success: false, Error: "invalid session"})
			continue
		}
		results = append(results, h.syncOneLocalSession(r.Context(), a, access, sess))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// syncOneLocalSession applies one offline session's position to the caller's
// media progress and returns the ABS-shaped per-session result. It never
// panics and never propagates an error to fail a batch.
func (h *Handler) syncOneLocalSession(ctx context.Context, a ctxAuth, access catalog.AccessFilter, sess localPlaybackSession) localSyncResult {
	res := localSyncResult{ID: sess.ID}

	// Podcast episode sessions are out of scope for silo's audiobook-only
	// catalog. Accept them as a no-op success so the client clears its queue.
	if sess.EpisodeID != nil && *sess.EpisodeID != "" {
		res.Success = true
		return res
	}
	if sess.LibraryItemID == "" {
		res.Error = "Media item not found"
		return res
	}

	// Ownership / existence / access gate — the item must be visible to the
	// caller. Mirrors handleSessionSync's access check.
	if h.deps.MediaStore == nil {
		res.Error = "Media item not found"
		return res
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(ctx, sess.LibraryItemID, access)
	if err != nil || item == nil {
		if err != nil {
			slog.WarnContext(ctx, "abs local session sync: media lookup failed", "component", "audiobooks",
				"library_item_id", sess.LibraryItemID, "error", err)
		}
		res.Error = "Media item not found"
		return res
	}

	res.Success = true

	// Persist the offline resume position into user_watch_progress.
	//
	// For an existing row we use UpdateProgressPosition (not a full upsert) so a
	// stale offline tick can't overwrite is_finished / progress_pct the user set
	// explicitly — it advances position monotonically and skips completed rows.
	//
	// But UpdateProgressPosition is UPDATE-only: when a book was listened to
	// entirely offline no row exists yet, so it would affect zero rows, drop the
	// position, and still report ProgressSynced=true — the client then clears its
	// local session with nothing saved. Create the row in that case so the resume
	// point survives.
	if h.deps.ProgressStore != nil {
		var syncErr error
		existing, getErr := h.deps.ProgressStore.GetProgress(ctx, a.UserID, a.ProfileID, sess.LibraryItemID)
		if getErr == nil && existing == nil {
			syncErr = h.deps.ProgressStore.UpsertProgress(ctx, ProgressRow{
				UserID:         a.UserID,
				ProfileID:      a.ProfileID,
				ContentID:      sess.LibraryItemID,
				CurrentSeconds: sess.CurrentTime,
				// For audiobooks MediaItem.Runtime holds total seconds (set by the
				// scanner), matching what the ABS libraries handler reads.
				DurationSeconds: float64(item.Runtime),
				UpdatedAt:       time.Now(),
			})
		} else {
			syncErr = h.deps.ProgressStore.UpdateProgressPosition(
				ctx, a.UserID, a.ProfileID, sess.LibraryItemID, sess.CurrentTime,
			)
		}
		if syncErr != nil {
			slog.WarnContext(ctx, "abs local session sync: persist progress position failed", "component", "audiobooks",
				"library_item_id", sess.LibraryItemID, "error", syncErr)
		} else {
			res.ProgressSynced = true
			// Realtime push so other connected clients see the caught-up
			// position — same event handleSessionSync emits.
			h.publish(a.UserID, "user_item_progress_updated", map[string]any{
				"data": map[string]any{
					"libraryItemId": sess.LibraryItemID,
					"currentTime":   sess.CurrentTime,
				},
			})
		}
	}
	return res
}
