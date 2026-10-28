package handlers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The lifecycle sweep ends sessions no API server holds in memory: one whose
// server died or restarted while its client went away, or whose end was
// interrupted before it recorded history. A live row a server keeps syncing
// means that server's own reaper decides; otherwise the sweep ends an idle
// session through the same durable claim the reaper uses, revalidated inside
// the claim, so a session that saw activity after the sweep looked keeps
// playing. docs/architecture/playback-lifecycle.md states the guarantee.

const (
	lifecycleSweepInterval = 15 * time.Second
	// A live row synced this recently means a server holds the session.
	lifecycleHeldGrace = 45 * time.Second
	// How long a session nobody holds may stay idle before it is ended:
	// the reaper's paused grace, and its idle floor for a remote transport
	// while playing, which also outlasts a rolling restart.
	lifecyclePlayingGrace = 5 * time.Minute
	lifecyclePausedGrace  = playback.DefaultPausedSessionGrace
	// An Audiobookshelf (beta) session nobody holds ends after the ABS
	// abandoned-session window.
	lifecycleABSGrace = 24 * time.Hour
	// Stopped rows are kept this long so a late start or pause for the
	// same session announces nothing: longer than an attempt's life.
	lifecycleTombstoneRetention = 25 * time.Hour
	lifecycleSweepBatch         = 100
)

// lifecycleCandidate is a live lifecycle row no server has synced lately.
type lifecycleCandidate struct {
	sessionID    string
	frontend     string
	userID       int
	profileID    string
	paused       bool
	activity     time.Time
	attemptFound bool
	attemptLive  bool
	finalized    bool
	stopID       string
}

// StartLifecycleSweep runs the sweep on every API server until ctx ends.
func (h *PlaybackHandler) StartLifecycleSweep(ctx context.Context, pool *pgxpool.Pool) {
	if h == nil || pool == nil || ctx == nil {
		return
	}
	go func() {
		// Servers start together after a deploy; spread their sweeps.
		jitter := time.Duration(time.Now().UnixNano() % int64(lifecycleSweepInterval))
		timer := time.NewTimer(lifecycleSweepInterval + jitter)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			sweepCtx, cancel := context.WithTimeout(ctx, lifecycleSweepInterval)
			h.sweepLifecycle(sweepCtx, pool, time.Now())
			cancel()
			timer.Reset(lifecycleSweepInterval)
		}
	}()
}

func (h *PlaybackHandler) sweepLifecycle(ctx context.Context, pool *pgxpool.Pool, now time.Time) {
	if _, err := pool.Exec(ctx, `DELETE FROM playback_session_lifecycle WHERE state = 'stopped' AND updated_at < $1`,
		now.Add(-lifecycleTombstoneRetention)); err != nil {
		slog.WarnContext(ctx, "lifecycle tombstone cleanup failed", "component", "api", "error", err)
	}
	candidates, err := loadLifecycleCandidates(ctx, pool, now)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle sweep failed", "component", "api", "error", err)
		return
	}
	var unheld []lifecycleCandidate
	var native []playback.Session
	for _, c := range candidates {
		if _, err := h.sessionMgr.GetSession(c.sessionID); err == nil {
			continue // held here: this server's reaper decides
		}
		unheld = append(unheld, c)
		if c.attemptLive {
			native = append(native, playback.Session{ID: c.sessionID, UserID: c.userID, ProfileID: c.profileID})
		}
	}
	// One lookup reads node delivery for every native candidate. Without it
	// they wait for the next sweep rather than expire on a guess.
	deliveries, err := h.deliveryActivity(ctx, native)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle sweep could not read node delivery", "component", "api", "sessions", len(native), "error", err)
	}
	for _, c := range unheld {
		if c.attemptLive && err != nil {
			continue
		}
		h.sweepLifecycleCandidate(ctx, pool, c, deliveries[c.sessionID], now)
	}
}

func loadLifecycleCandidates(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]lifecycleCandidate, error) {
	rows, err := pool.Query(ctx, `
		WITH live AS (
			SELECT lc.session_id, lc.frontend, lc.user_id, lc.profile_id, lc.state = 'paused' AS paused,
				GREATEST(lc.updated_at, COALESCE(lc.activity_at, lc.updated_at), COALESCE(a.updated_at, lc.updated_at)) AS activity,
				a.session_id IS NOT NULL AS attempt_found, a.session_id IS NOT NULL AND a.stopped_at IS NULL AS attempt_live,
				COALESCE((a.stop_receipt->>'finalized')::boolean, FALSE) AS finalized, COALESCE(a.stop_receipt->>'stop_id', '') AS stop_id
			FROM playback_session_lifecycle lc
			LEFT JOIN playback_v3_attempts a ON a.session_id = lc.session_id AND a.expires_at > $1
			WHERE lc.state <> 'stopped' AND lc.updated_at < $2
			  AND NOT EXISTS (SELECT 1 FROM playback_sessions_sync s
				WHERE s.session_id = lc.session_id::text AND s.last_sync_at > $2)
		)
		-- Only rows the sweep can act on now: an attempt someone stopped, or a
		-- session idle past its grace. Rows still active never fill the batch.
		SELECT session_id::text, frontend, user_id, profile_id, paused, activity, attempt_found, attempt_live, finalized, stop_id
		FROM live
		WHERE (attempt_found AND NOT attempt_live)
		   OR activity < $1 - CASE
				WHEN frontend = 'abs' THEN $3::interval
				WHEN paused THEN $4::interval
				ELSE $5::interval END
		ORDER BY activity
		LIMIT $6`, now, now.Add(-lifecycleHeldGrace), lifecycleABSGrace, lifecyclePausedGrace, lifecyclePlayingGrace, lifecycleSweepBatch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleCandidate, error) {
		var c lifecycleCandidate
		err := row.Scan(&c.sessionID, &c.frontend, &c.userID, &c.profileID, &c.paused, &c.activity,
			&c.attemptFound, &c.attemptLive, &c.finalized, &c.stopID)
		return c, err
	})
}

func (h *PlaybackHandler) sweepLifecycleCandidate(ctx context.Context, pool *pgxpool.Pool, c lifecycleCandidate, delivered, now time.Time) {
	grace := lifecyclePlayingGrace
	if c.paused {
		grace = lifecyclePausedGrace
	}
	idleBefore := now.Add(-grace)
	switch {
	case c.attemptLive:
		h.expireIdleNativeSession(ctx, pool, c, delivered, idleBefore)
	case c.attemptFound && !c.finalized:
		h.finishInterruptedNativeEnd(ctx, c.sessionID)
	case c.attemptFound:
		// The attempt's end was finalized without stopping the lifecycle
		// (its writer failed, or ended before the upgrade): record it now.
		h.endLifecycleFromFacts(ctx, c.sessionID, endReasonForStop(c.sessionID, c.stopID))
	case c.frontend == LifecycleFrontendJellyfin:
		h.expireIdleJellyfinSession(ctx, pool, c, idleBefore)
	case c.frontend == LifecycleFrontendABS:
		if c.activity.Before(now.Add(-lifecycleABSGrace)) {
			h.endLifecycleFromFacts(ctx, c.sessionID, PlaybackEndExpired)
		}
	case c.activity.Before(idleBefore):
		// A native session whose attempt is gone (expired and cleaned up).
		h.endLifecycleFromFacts(ctx, c.sessionID, PlaybackEndExpired)
	}
}

// expireIdleNativeSession ends a native session no server holds once its
// client and its media have both been idle past the grace.
func (h *PlaybackHandler) expireIdleNativeSession(ctx context.Context, pool *pgxpool.Pool, c lifecycleCandidate, delivered, idleBefore time.Time) {
	// A node can keep serving media while nothing reports progress (a Cast
	// receiver whose sender slept). Its delivery counts as activity, and is
	// remembered on the lifecycle row beyond the node's short record.
	if !delivered.IsZero() {
		recordLifecycleActivity(ctx, pool, c.sessionID, delivered)
		if delivered.After(idleBefore) {
			return
		}
	}
	if c.activity.After(idleBefore) {
		return
	}
	expirer, ok := h.PlanStoreV3.(interface {
		ExpireIdleAttempt(ctx context.Context, sessionID, stopID string, idleBefore time.Time) (bool, error)
	})
	if !ok {
		return
	}
	stopped, err := expirer.ExpireIdleAttempt(ctx, c.sessionID, expiredStopID(c.sessionID), idleBefore)
	if err != nil || !stopped {
		if err != nil {
			slog.WarnContext(ctx, "lifecycle sweep could not expire an idle session", "component", "api", "session", c.sessionID, "error", err)
		}
		return
	}
	// Only a session this claim ended loses its tokens: one that saw
	// progress after the sweep looked keeps playing.
	h.StreamDeny.Deny(ctx, c.sessionID)
	slog.InfoContext(ctx, "expired a playback session no server holds", "component", "api", "session", c.sessionID, "playback_session_id", c.sessionID)
	h.finishInterruptedNativeEnd(ctx, c.sessionID)
}

// deliveryActivity reads when a node last delivered media for each session.
func (h *PlaybackHandler) deliveryActivity(ctx context.Context, sessions []playback.Session) (map[string]time.Time, error) {
	reader, ok := h.sessionMgr.(interface {
		DeliveryActivity(context.Context, []playback.Session) (map[string]time.Time, error)
	})
	if !ok || len(sessions) == 0 {
		return nil, nil
	}
	return reader.DeliveryActivity(ctx, sessions)
}

// recordLifecycleActivity remembers activity the sweep saw outside the
// lifecycle row, so the row stops being a candidate until it is idle again.
func recordLifecycleActivity(ctx context.Context, pool *pgxpool.Pool, sessionID string, at time.Time) {
	if _, err := pool.Exec(ctx, `UPDATE playback_session_lifecycle SET activity_at = GREATEST(activity_at, $2) WHERE session_id = $1`,
		sessionID, at); err != nil {
		slog.WarnContext(ctx, "lifecycle sweep could not record a session's activity", "component", "api", "session", sessionID, "error", err)
	}
}

// finishInterruptedNativeEnd finalizes a stopped attempt nobody finished,
// once its finalization lease is free, from the attempt row.
func (h *PlaybackHandler) finishInterruptedNativeEnd(ctx context.Context, sessionID string) {
	store, ok := h.PlanStoreV3.(playback.ProgressStoreV3)
	if !ok {
		return
	}
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	if err != nil || record.StoppedAt == nil {
		return
	}
	if !claimStopFinalization(ctx, store, sessionID, record.StopID) {
		return
	}
	receipt, _, err := store.StopAttempt(ctx, sessionID, record.StopID, nil)
	if err != nil {
		return
	}
	historyID, err := h.finishStop(ctx, record, sessionID, receipt.Accepted, endReasonForStop(sessionID, receipt.StopID))
	if err != nil {
		// Left unfinalized: the lease lapses and a later sweep retries.
		slog.WarnContext(ctx, "lifecycle sweep could not finish a session's end", "component", "api", "session", sessionID, "error", err)
		return
	}
	claim := nativeEndClaim{store: store, record: record, receipt: receipt, owned: true}
	claim.finish(ctx, sessionID, historyID)
	h.forgetProgressSideEffectLock(sessionID)
}

// expireIdleJellyfinSession ends a Jellyfin play no server holds through the
// shared compatibility expiry claim, which marks its play row terminal so no
// report revives it.
func (h *PlaybackHandler) expireIdleJellyfinSession(ctx context.Context, pool *pgxpool.Pool, c lifecycleCandidate, idleBefore time.Time) {
	if c.activity.After(idleBefore) {
		return
	}
	claimer, ok := h.sessionMgr.(interface {
		ClaimCompatExpiry(context.Context, []playback.SessionExpiryCandidate) (map[string]bool, bool, error)
	})
	if !ok {
		return
	}
	claimed, configured, err := claimer.ClaimCompatExpiry(ctx, []playback.SessionExpiryCandidate{{
		Session:        playback.Session{ID: c.sessionID, UserID: c.userID, ProfileID: c.profileID},
		InactiveBefore: idleBefore,
	}})
	if err != nil {
		slog.WarnContext(ctx, "lifecycle sweep could not claim a Jellyfin expiry", "component", "api", "session", c.sessionID, "error", err)
		return
	}
	if configured && !claimed[c.sessionID] {
		// A report arrived after idleBefore, and the play goes on. Recording
		// that bound moves the row behind sessions idle for longer, so live
		// plays never fill the sweep's batch.
		recordLifecycleActivity(ctx, pool, c.sessionID, idleBefore)
		return
	}
	h.endLifecycleFromFacts(ctx, c.sessionID, PlaybackEndExpired)
}

// endLifecycleFromFacts records the end of a session nothing richer is left
// for: the row it recorded when it started, at the last position the shared
// state knows.
func (h *PlaybackHandler) endLifecycleFromFacts(ctx context.Context, sessionID string, reason PlaybackEndReason) {
	reader, ok := h.AdminStore.(liveSessionReader)
	if !ok {
		return
	}
	session, err := reader.LiveSession(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "lifecycle sweep could not read a session's start", "component", "api", "session", sessionID, "error", err)
		}
		return
	}
	// The attempt's last accepted sample is the play's position, including a
	// seek back past where the shared row last saw it.
	if record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID); err == nil && record.LastSample != nil {
		session.Position = record.LastSample.Position
	}
	h.recordAdminStopHistory(ctx, session, reason)
}
