package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// The paused timeout ends a native session that has stayed paused longer
// than the administrator allows (playback.paused_session_timeout_minutes),
// so an abandoned pause stops holding a stream slot and its transcode. It
// runs in every lifecycle sweep, on every API server, and ends only sessions
// whose client is still reporting: one that went quiet is the idle expiry's.
// docs/architecture/playback-lifecycle.md describes it.

const (
	// pausedTimeoutSettingTTL is how long a server reuses the timeout it
	// read: an administrator's change applies within this plus one sweep.
	pausedTimeoutSettingTTL = time.Minute
	// pausedTimeoutPagesPerSweep bounds the candidate pages one sweep reads.
	// The pages continue from where the last sweep stopped, so rows the
	// sweep reads and leaves (audio-only files, unreadable files) cannot
	// hold eligible sessions behind them.
	pausedTimeoutPagesPerSweep = 3
	// pausedTimeoutTeardownTimeout bounds the teardown after a won claim or
	// for a stale copy. It runs detached from the sweep's deadline, so a sweep
	// that runs out of time cannot leave a stopped session's copy behind.
	pausedTimeoutTeardownTimeout = 30 * time.Second
)

// pausedAttemptExpirer ends an attempt left paused too long: it stops the
// live attempt under stopID only while its last accepted sample is paused,
// the session has been paused for at least timeout, no Cast receiver renders
// it and no open Watch Together room lists it, judged under the lock that
// orders progress, so a resume that lands first keeps it live. It reports
// whether this call stopped the attempt. planstore.Postgres provides it.
type pausedAttemptExpirer interface {
	ExpirePausedAttempt(ctx context.Context, sessionID, stopID string, mediaFileID int, timeout time.Duration) (bool, error)
}

// pausedTimeoutCandidate is a native session paused past the timeout whose
// client is still reporting.
type pausedTimeoutCandidate struct {
	sessionID   string
	mediaFileID int
	// pausedSince is the lifecycle row's updated_at, the page cursor.
	pausedSince time.Time
}

// pausedTimeoutCursor is where the next candidate page starts: after the
// row with this (lifecycle updated_at, session id).
type pausedTimeoutCursor struct {
	pausedSince time.Time
	sessionID   string
}

// pausedSessionTimeout is the configured timeout, zero when it is off, read
// through a per-server cache that keeps a value for pausedTimeoutSettingTTL.
// ok is false while no read of the setting has succeeded: the default would
// end sessions an administrator may have kept by turning the timeout off.
func (h *PlaybackHandler) pausedSessionTimeout(ctx context.Context) (timeout time.Duration, ok bool) {
	h.pausedTimeoutOnce.Do(func() {
		var settings config.SettingReader
		if h.SettingsRepo != nil {
			settings = h.SettingsRepo
		}
		h.pausedTimeoutSetting = config.NewCachedSetting(settings, playback.PausedSessionTimeoutSettingKey,
			pausedTimeoutSettingTTL, playback.ParsePausedSessionTimeout)
	})
	return h.pausedTimeoutSetting.Lookup(ctx)
}

// sweepPausedTimeouts drops the copies this server holds of sessions another
// server's paused timeout ended, then ends the sessions paused past the
// timeout that this server holds, or that no server holds. The copies go
// even while the timeout is off: another server may have stopped them before
// the change reached it.
func (h *PlaybackHandler) sweepPausedTimeouts(ctx context.Context, pool *pgxpool.Pool) {
	heldIDs := h.heldNativeSessionIDs()
	h.dropCopiesPausedOutElsewhere(ctx, pool, heldIDs)
	timeout, ok := h.pausedSessionTimeout(ctx)
	if !ok || timeout <= 0 {
		return
	}
	expirer, ok := h.PlanStoreV3.(pausedAttemptExpirer)
	if !ok || h.fileResolver == nil {
		// Without the files, audio-only sessions can't be told apart.
		return
	}
	h.pausedTimeoutCursorMu.Lock()
	defer h.pausedTimeoutCursorMu.Unlock()
	for range pausedTimeoutPagesPerSweep {
		candidates, err := loadPausedTimeoutCandidates(ctx, pool, timeout, heldIDs, h.pausedTimeoutCursor)
		if err != nil {
			slog.WarnContext(ctx, "paused timeout sweep failed", "component", "api", "error", err)
			return
		}
		files := h.pausedCandidateFiles(ctx, candidates)
		for _, c := range candidates {
			if ctx.Err() != nil {
				// The sweep ran out of time: the next one resumes at this
				// candidate, not past the rest of the page.
				return
			}
			h.endPausedTimeout(ctx, expirer, c, files[c.mediaFileID], timeout)
			if ctx.Err() != nil {
				// The deadline may have cut this claim short: retry it next.
				return
			}
			h.pausedTimeoutCursor = &pausedTimeoutCursor{pausedSince: c.pausedSince, sessionID: c.sessionID}
		}
		if len(candidates) < lifecycleSweepBatch {
			// The end of the list: the next sweep starts from the oldest.
			h.pausedTimeoutCursor = nil
			return
		}
	}
}

// heldNativeSessionIDs is the ids of this server's live copies that can
// have an attempt row.
func (h *PlaybackHandler) heldNativeSessionIDs() []string {
	lister, ok := h.sessionMgr.(interface{ AllSessions() []*playback.Session })
	if !ok {
		return nil
	}
	var ids []string
	for _, s := range lister.AllSessions() {
		if s == nil || s.IsJellyfinCompat {
			continue
		}
		if _, err := uuid.Parse(s.ID); err == nil {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// dropCopiesPausedOutElsewhere drops this server's copies of sessions whose
// attempt another server stopped under pausedTimeoutStopID. That server
// claims only a session nobody synced lately, so a holder whose sync stalled
// can still hold a copy, with its transcode and its slots, until its own
// reaper. The end is finished from this copy first when nobody has (under the
// finalization lease, so its writers run once), then the copy goes.
func (h *PlaybackHandler) dropCopiesPausedOutElsewhere(ctx context.Context, pool *pgxpool.Pool, ids []string) {
	if len(ids) == 0 {
		return
	}
	rows, err := pool.Query(ctx, `
		SELECT session_id::text, stop_id::text FROM playback_v3_attempts
		WHERE session_id = ANY($1::uuid[]) AND stopped_at IS NOT NULL AND stop_id IS NOT NULL`, ids)
	if err != nil {
		slog.WarnContext(ctx, "paused timeout sweep could not read held sessions' attempts", "component", "api", "error", err)
		return
	}
	type stop struct{ sessionID, stopID string }
	stops, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stop, error) {
		var s stop
		err := row.Scan(&s.sessionID, &s.stopID)
		return s, err
	})
	if err != nil {
		slog.WarnContext(ctx, "paused timeout sweep could not read held sessions' attempts", "component", "api", "error", err)
		return
	}
	for _, s := range stops {
		if s.stopID != pausedTimeoutStopID(s.sessionID) {
			continue // other ends reach this copy through their own paths
		}
		teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), pausedTimeoutTeardownTimeout)
		h.finishInterruptedNativeEnd(teardown, s.sessionID)
		h.dropUnfinishedPausedCopy(teardown, s.sessionID)
		cancel()
	}
}

// loadPausedTimeoutCandidates selects one page, oldest pause first and after
// the cursor, of the native sessions the lifecycle has said paused for at
// least timeout, whose live attempt last accepted a paused sample within
// PausedStreamSlotGrace (the client is still reporting), that no Cast
// receiver renders and no open Watch Together room holds, and that this
// server holds (held) or no server synced within lifecycleHeldGrace. A
// session another server holds is that server's to end. The claim re-checks
// the pause and the exemptions under the attempt's lock.
func loadPausedTimeoutCandidates(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration, held []string, after *pausedTimeoutCursor) ([]pausedTimeoutCandidate, error) {
	var afterSince, afterID any
	if after != nil {
		afterSince, afterID = after.pausedSince, after.sessionID
	}
	if held == nil {
		held = []string{} // NULL would never match ANY
	}
	rows, err := pool.Query(ctx, `
		SELECT lc.session_id::text, a.effective_media_file_id, lc.updated_at
		FROM playback_session_lifecycle lc
		JOIN playback_v3_attempts a ON a.session_id = lc.session_id AND a.expires_at > NOW() AND a.stopped_at IS NULL
		WHERE lc.frontend = 'native' AND lc.state = 'paused'
		  AND lc.updated_at <= NOW() - make_interval(secs => $1)
		  AND ($6::timestamptz IS NULL OR (lc.updated_at, lc.session_id) > ($6::timestamptz, $7::uuid))
		  AND COALESCE((a.last_sample->>'is_paused')::boolean, false)
		  AND a.updated_at >= NOW() - make_interval(secs => $2)
		  AND COALESCE(a.normalized_request #>> '{client_playback_context,output,current_sink}', '') <> $4
		  AND (lc.session_id = ANY($8::uuid[]) OR NOT EXISTS (SELECT 1 FROM playback_sessions_sync s
				WHERE s.session_id = lc.session_id::text AND s.last_sync_at > NOW() - make_interval(secs => $3)))
		  AND `+playback.NotInOpenWatchTogetherRoomSQL+`
		ORDER BY lc.updated_at, lc.session_id
		LIMIT $5`, timeout.Seconds(), playback.PausedStreamSlotGrace.Seconds(), lifecycleHeldGrace.Seconds(),
		playback.CastReceiverSinkV3, lifecycleSweepBatch, afterSince, afterID, held)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (pausedTimeoutCandidate, error) {
		var c pausedTimeoutCandidate
		err := row.Scan(&c.sessionID, &c.mediaFileID, &c.pausedSince)
		return c, err
	})
}

// endPausedTimeout claims one candidate's end and finishes it: from the live
// session when this server holds it, from the attempt row otherwise. An
// audio-only file (an audiobook or podcast) is never ended by it, and a
// candidate whose file could not be read (nil) waits for a later sweep.
func (h *PlaybackHandler) endPausedTimeout(ctx context.Context, expirer pausedAttemptExpirer, c pausedTimeoutCandidate, file *models.MediaFile, timeout time.Duration) {
	if file == nil || file.IsAudioOnly() {
		return
	}
	stopped, err := expirer.ExpirePausedAttempt(ctx, c.sessionID, pausedTimeoutStopID(c.sessionID), c.mediaFileID, timeout)
	if err != nil || !stopped {
		if err != nil {
			slog.WarnContext(ctx, "paused timeout sweep could not end a session", "component", "api", "session", c.sessionID, "error", err)
		}
		return
	}
	teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), pausedTimeoutTeardownTimeout)
	defer cancel()
	h.StreamDeny.Deny(teardown, c.sessionID)
	slog.InfoContext(teardown, "ended a playback session paused too long", "component", "api", "session", c.sessionID,
		"playback_session_id", c.sessionID, "timeout", timeout)
	h.finishInterruptedNativeEnd(teardown, c.sessionID)
	h.dropUnfinishedPausedCopy(teardown, c.sessionID)
}

// pausedCandidateFiles reads a page's media files by id: in one lookup when
// the resolver can batch (the catalog's file repository does), otherwise, or
// when the batch fails, once per distinct file. A file it cannot read is left
// out, so its session waits.
func (h *PlaybackHandler) pausedCandidateFiles(ctx context.Context, candidates []pausedTimeoutCandidate) map[int]*models.MediaFile {
	ids := make([]int, 0, len(candidates))
	seen := make(map[int]bool, len(candidates))
	for _, c := range candidates {
		if !seen[c.mediaFileID] {
			seen[c.mediaFileID] = true
			ids = append(ids, c.mediaFileID)
		}
	}
	files := make(map[int]*models.MediaFile, len(ids))
	if batch, ok := h.fileResolver.(interface {
		GetByIDs(context.Context, []int) ([]*models.MediaFile, error)
	}); ok {
		found, err := batch.GetByIDs(ctx, ids)
		if err == nil {
			for _, file := range found {
				if file != nil {
					files[file.ID] = file
				}
			}
			return files
		}
		// One unreadable file fails the whole batch; read them one by one so
		// only that file's session waits.
		slog.WarnContext(ctx, "paused timeout sweep could not batch-read its sessions' files", "component", "api", "error", err)
	}
	for _, id := range ids {
		file, err := h.fileResolver.GetByID(ctx, id)
		if err != nil {
			slog.WarnContext(ctx, "paused timeout sweep could not read a session's file", "component", "api", "file", id, "error", err)
			continue
		}
		if file != nil {
			files[id] = file
		}
	}
	return files
}

// dropUnfinishedPausedCopy drops the copy held here of a session the paused
// timeout ended when finishing the end did not tear it down (another caller
// holds the finalization lease, or the writers failed). The copy goes now,
// without history, so its transcode and slot do not wait for the reaper.
// The stopped attempt is finished later from its row by the lifecycle sweep,
// under the same lease, so history is written once; removing the live row
// lets the next sweep take it up at once.
func (h *PlaybackHandler) dropUnfinishedPausedCopy(ctx context.Context, sessionID string) {
	if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
		return
	}
	if err := h.dropLiveSession(ctx, sessionID, false); err != nil {
		slog.WarnContext(ctx, "paused timeout sweep could not drop a session's copy", "component", "api", "session", sessionID, "error", err)
		return
	}
	if h.AdminStore == nil {
		return
	}
	if err := h.AdminStore.DeleteSession(ctx, sessionID); err != nil {
		slog.WarnContext(ctx, "paused timeout sweep could not remove a session's live row", "component", "api", "session", sessionID, "error", err)
	}
}
