package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// The paused timeout ends a native session that has stayed paused longer
// than the administrator allows (playback.paused_session_timeout_minutes),
// so an abandoned pause stops holding a stream slot and its transcode. It
// runs in every lifecycle sweep, on every API server, and ends only sessions
// whose client is still reporting: one that went quiet is the idle expiry's.
// docs/architecture/playback-lifecycle.md describes it.

// castReceiverSinkV3 is output.current_sink for a play a Cast receiver
// renders. The sender keeps it going without the viewer, so it is exempt.
const castReceiverSinkV3 = "cast_receiver"

// pausedTimeoutCandidate is a native session paused past the timeout whose
// client is still reporting.
type pausedTimeoutCandidate struct {
	sessionID   string
	mediaFileID int
	// synced: some server synced the session's live row within
	// lifecycleHeldGrace, so whichever holds it ends it.
	synced bool
}

// pausedSessionTimeout is the configured timeout, zero when it is off. The
// setting is read through a per-server cache that lives for one sweep.
func (h *PlaybackHandler) pausedSessionTimeout(ctx context.Context) time.Duration {
	h.pausedTimeoutOnce.Do(func() {
		var settings config.SettingReader
		if h.SettingsRepo != nil {
			settings = h.SettingsRepo
		}
		h.pausedTimeoutSetting = config.NewCachedSetting(settings, playback.PausedSessionTimeoutSettingKey,
			lifecycleSweepInterval, playback.ParsePausedSessionTimeout)
	})
	return h.pausedTimeoutSetting.Get(ctx)
}

// sweepPausedTimeouts ends the sessions paused past the timeout that this
// server holds, or that no server holds.
func (h *PlaybackHandler) sweepPausedTimeouts(ctx context.Context, pool *pgxpool.Pool) {
	timeout := h.pausedSessionTimeout(ctx)
	if timeout <= 0 {
		return
	}
	expirer, ok := h.PlanStoreV3.(playback.PausedAttemptExpirerV3)
	if !ok {
		return
	}
	candidates, err := loadPausedTimeoutCandidates(ctx, pool, timeout)
	if err != nil {
		slog.WarnContext(ctx, "paused timeout sweep failed", "component", "api", "error", err)
		return
	}
	for _, c := range candidates {
		h.endPausedTimeout(ctx, expirer, c, timeout)
	}
}

// loadPausedTimeoutCandidates selects, oldest pause first, the native sessions
// the lifecycle has said paused for at least timeout, whose live attempt last
// accepted a paused sample within PausedStreamSlotGrace (the client is still
// reporting), that no Cast receiver renders and no open Watch Together room
// holds. The claim re-checks the pause under the attempt's lock.
func loadPausedTimeoutCandidates(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) ([]pausedTimeoutCandidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT lc.session_id::text, a.effective_media_file_id,
			EXISTS (SELECT 1 FROM playback_sessions_sync s
				WHERE s.session_id = lc.session_id::text AND s.last_sync_at > NOW() - make_interval(secs => $3))
		FROM playback_session_lifecycle lc
		JOIN playback_v3_attempts a ON a.session_id = lc.session_id AND a.expires_at > NOW() AND a.stopped_at IS NULL
		WHERE lc.frontend = 'native' AND lc.state = 'paused'
		  AND lc.updated_at <= NOW() - make_interval(secs => $1)
		  AND COALESCE((a.last_sample->>'is_paused')::boolean, false)
		  AND a.updated_at >= NOW() - make_interval(secs => $2)
		  AND COALESCE(a.normalized_request #>> '{client_playback_context,output,current_sink}', '') <> $4
		  AND NOT EXISTS (SELECT 1 FROM watch_together_rooms r, jsonb_each(r.runtime->'members') m
			WHERE r.phase <> 'ended' AND m.value->>'session_id' = lc.session_id::text)
		ORDER BY lc.updated_at
		LIMIT $5`, timeout.Seconds(), playback.PausedStreamSlotGrace.Seconds(), lifecycleHeldGrace.Seconds(),
		castReceiverSinkV3, lifecycleSweepBatch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (pausedTimeoutCandidate, error) {
		var c pausedTimeoutCandidate
		err := row.Scan(&c.sessionID, &c.mediaFileID, &c.synced)
		return c, err
	})
}

// endPausedTimeout claims one candidate's end and finishes it: from the live
// session when this server holds it, from the attempt row otherwise. A
// session another server holds is left to that server's sweep, and an
// audio-only file (an audiobook or podcast) is never ended by it.
func (h *PlaybackHandler) endPausedTimeout(ctx context.Context, expirer playback.PausedAttemptExpirerV3, c pausedTimeoutCandidate, timeout time.Duration) {
	if _, err := h.sessionMgr.GetSession(c.sessionID); err != nil && c.synced {
		return
	}
	if h.fileResolver == nil {
		return
	}
	file, err := h.fileResolver.GetByID(ctx, c.mediaFileID)
	if err != nil || file == nil {
		if err != nil {
			slog.WarnContext(ctx, "paused timeout sweep could not read a session's file", "component", "api", "session", c.sessionID, "error", err)
		}
		return
	}
	if file.IsAudioOnly() {
		return
	}
	stopped, err := expirer.ExpirePausedAttempt(ctx, c.sessionID, pausedTimeoutStopID(c.sessionID), timeout)
	if err != nil || !stopped {
		if err != nil {
			slog.WarnContext(ctx, "paused timeout sweep could not end a session", "component", "api", "session", c.sessionID, "error", err)
		}
		return
	}
	h.StreamDeny.Deny(ctx, c.sessionID)
	slog.InfoContext(ctx, "ended a playback session paused too long", "component", "api", "session", c.sessionID,
		"playback_session_id", c.sessionID, "timeout", timeout)
	h.finishInterruptedNativeEnd(ctx, c.sessionID)
}
