package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

const (
	// nodeDeadTimeout is how long a node can go without a heartbeat before
	// its sessions are purged.
	nodeDeadTimeout = 45 * time.Second

	// nodeHeartbeatCleanup is how long before stale heartbeat rows
	// themselves are deleted (longer than nodeDeadTimeout to avoid flapping).
	nodeHeartbeatCleanup = 5 * time.Minute

	// activeSessionGrace is the staleness threshold for active (not paused)
	// sessions based on last_sync_at.
	activeSessionGrace = 45 * time.Second

	// pausedSessionGrace is the staleness threshold for paused sessions.
	pausedSessionGrace = 2 * time.Minute

	// cleanupInterval is how often the cleanup ticker fires.
	cleanupInterval = 15 * time.Second

	// absStaleOpenSessionGrace closes audiobook playback sessions that stopped
	// syncing without an explicit /close (abandoned playback) so they don't
	// linger as "open" forever and inflate listening-stats aggregation.
	absStaleOpenSessionGrace = 24 * time.Hour

	// absSessionPruneInterval throttles the abandoned-session sweep: it's a slow-moving
	// concern, so it runs hourly rather than on every 15s cleanup tick.
	absSessionPruneInterval = time.Hour
)

// SessionCleaner removes stale playback sessions and dead node records.
type SessionCleaner struct {
	pool      *pgxpool.Pool
	EventBus  cache.EventBus
	EventsHub *evt.Hub
	stop      chan struct{}

	// lastABSSessionPrune gates the hourly abs_playback_sessions retention
	// sweep. Guarded by absPruneMu because CleanStale is also invoked from the
	// shutdown path while the ticker goroutine is still running.
	absPruneMu          sync.Mutex
	lastABSSessionPrune time.Time
}

// NewSessionCleaner creates a SessionCleaner. The graceSeconds parameter is
// accepted for backwards compatibility but ignored — grace periods are now
// fixed at 45s (active) and 2m (paused).
func NewSessionCleaner(pool *pgxpool.Pool, graceSeconds int) *SessionCleaner {
	return &SessionCleaner{
		pool: pool,
		stop: make(chan struct{}),
	}
}

// Start begins the background cleanup loop, firing every 15 seconds.
func (c *SessionCleaner) Start() {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-c.stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if deleted, err := c.CleanStale(ctx); err != nil {
					slog.Error("session cleanup error", "error", err)
				} else if deleted > 0 {
					slog.Debug("cleaned stale sessions", "count", deleted)
				}
				cancel()
			}
		}
	}()
}

// Stop signals the cleanup loop to stop.
func (c *SessionCleaner) Stop() {
	close(c.stop)
}

// CleanStale performs a full cleanup pass:
// 1. Purge sessions from dead nodes (heartbeat stale > 45s)
// 2. Remove stale heartbeat rows (> 5 minutes)
// 3. Remove stale active sessions (last_sync_at > 45s)
// 4. Remove stale paused sessions (last_sync_at > 2 minutes)
func (c *SessionCleaner) CleanStale(ctx context.Context) (int, error) {
	var totalDeleted int64

	// 1. Purge sessions belonging to dead nodes.
	tag, err := c.pool.Exec(ctx, `
		DELETE FROM playback_sessions_sync
		WHERE reporting_node IN (
			SELECT node_id FROM node_heartbeats
			WHERE updated_at < NOW() - make_interval(secs => $1::double precision)
		)
	`, nodeDeadTimeout.Seconds())
	if err != nil {
		return 0, fmt.Errorf("purging dead node sessions: %w", err)
	}
	totalDeleted += tag.RowsAffected()

	// 2. Clean up stale heartbeat rows.
	if _, err := c.pool.Exec(ctx, `
		DELETE FROM node_heartbeats
		WHERE updated_at < NOW() - make_interval(secs => $1::double precision)
	`, nodeHeartbeatCleanup.Seconds()); err != nil {
		return int(totalDeleted), fmt.Errorf("cleaning stale heartbeats: %w", err)
	}

	// 3. Active sessions: 45s grace on last_sync_at.
	tag, err = c.pool.Exec(ctx, `
		DELETE FROM playback_sessions_sync
		WHERE is_paused = FALSE
		  AND last_sync_at < NOW() - make_interval(secs => $1::double precision)
	`, activeSessionGrace.Seconds())
	if err != nil {
		return int(totalDeleted), fmt.Errorf("cleaning stale active sessions: %w", err)
	}
	totalDeleted += tag.RowsAffected()

	// 4. Paused sessions: 2 minute grace on last_sync_at.
	tag, err = c.pool.Exec(ctx, `
		DELETE FROM playback_sessions_sync
		WHERE is_paused = TRUE
		  AND last_sync_at < NOW() - make_interval(secs => $1::double precision)
	`, pausedSessionGrace.Seconds())
	if err != nil {
		return int(totalDeleted), fmt.Errorf("cleaning stale paused sessions: %w", err)
	}
	totalDeleted += tag.RowsAffected()

	// 5. Audiobook session cleanup (hourly): close abandoned open sessions.
	// Closed rows are retained because the ABS stats endpoint currently has
	// all-time semantics and aggregates directly from abs_playback_sessions.
	// Kept off totalDeleted so it doesn't trigger the live-session
	// invalidation event. The due-check is mutex-guarded so the shutdown-path
	// CleanStale and the ticker can't race or double-run it.
	c.absPruneMu.Lock()
	pruneStartedAt := time.Now()
	previousABSSessionPrune := c.lastABSSessionPrune
	abndPruneDue := pruneStartedAt.Sub(c.lastABSSessionPrune) >= absSessionPruneInterval
	if abndPruneDue {
		c.lastABSSessionPrune = pruneStartedAt
	}
	c.absPruneMu.Unlock()
	if abndPruneDue {
		if err := c.closeAbandonedABSSessions(ctx); err != nil {
			slog.Warn("abs session cleanup failed", "error", err)
			c.absPruneMu.Lock()
			if c.lastABSSessionPrune.Equal(pruneStartedAt) {
				c.lastABSSessionPrune = previousABSSessionPrune
			}
			c.absPruneMu.Unlock()
		}
	}

	if totalDeleted > 0 && c.EventsHub != nil {
		if err := c.EventsHub.PublishJSON(
			ctx,
			evt.ChannelSessions,
			"sessions.replaced",
			nil,
			evt.PublishOptions{AdminOnly: true},
		); err != nil {
			return int(totalDeleted), fmt.Errorf("publishing playback cleanup invalidation: %w", err)
		}
	} else if c.EventBus != nil && totalDeleted > 0 {
		if err := c.EventBus.Publish(ctx, cache.ChannelPlayback, cache.Event{
			Type:    cache.EventPlaybackSessionsChanged,
			Payload: "cleanup",
		}); err != nil {
			return int(totalDeleted), fmt.Errorf("publishing playback cleanup invalidation: %w", err)
		}
	}

	return int(totalDeleted), nil
}

// closeAbandonedABSSessions closes abandoned audiobook playback sessions (no
// explicit /close, stopped syncing). It intentionally does not delete closed
// sessions: AggregateStats currently uses this table for all-time totals.
func (c *SessionCleaner) closeAbandonedABSSessions(ctx context.Context) error {
	if _, err := c.pool.Exec(ctx, `
		UPDATE abs_playback_sessions
		SET closed_at = now()
		WHERE closed_at IS NULL
		  AND COALESCE(last_sync_at, started_at) < NOW() - make_interval(secs => $1::double precision)
	`, absStaleOpenSessionGrace.Seconds()); err != nil {
		return fmt.Errorf("closing abandoned abs sessions: %w", err)
	}
	return nil
}
