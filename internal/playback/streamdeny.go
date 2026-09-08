package playback

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Stream deny marker: one Redis key per playback session that revokes every
// stream token minted for that session before the tokens expire. Stream tokens
// are self-contained JWTs (see docs/architecture/restart-resilient-playback.md),
// so without the marker a stopped, expired, or admin-terminated session could
// keep serving for up to MaxTokenTTL from any replica that reconstructs it.
//
// The marker outlives every token (its TTL is MaxTokenTTL) and is checked by
// every serve path that loads or reconstructs a session. Redis is not on the
// critical path: a lookup that fails or times out is treated as "not denied",
// with a rate-limited warning, so an unreachable Redis never blocks serving.
const (
	streamDenyKeyPrefix = "silo:streamauth:"
	streamDenyValue     = "deny"
	// streamDenyCacheTTL bounds Redis load to one GET per session per window
	// on each replica; it is also the longest a revocation can lag on a
	// replica that has just checked the same session.
	streamDenyCacheTTL = 2 * time.Second
	// streamDenyLookupTimeout keeps a slow Redis from stalling a segment
	// request; a lookup past it fails open like any other Redis error.
	streamDenyLookupTimeout = time.Second
	streamDenyWarnEvery     = time.Minute
	// streamDenyCacheSweepAt is the cache size past which expired entries are
	// swept on insert (at most once per cache window).
	streamDenyCacheSweepAt = 1024
)

// streamDenyClient is the slice of the go-redis client the marker needs.
type streamDenyClient interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Get(ctx context.Context, key string) *redis.StringCmd
}

// StreamDeny writes and checks session-deny markers. A nil *StreamDeny is a
// no-op: Deny does nothing and Denied is always false, so a deployment without
// Redis keeps the pre-marker behavior.
type StreamDeny struct {
	client streamDenyClient
	now    func() time.Time
	log    *slog.Logger

	mu        sync.Mutex
	cache     map[string]streamDenyEntry
	nextSweep time.Time
	lastWarn  time.Time
}

type streamDenyEntry struct {
	denied  bool
	expires time.Time
}

// NewStreamDeny returns the marker store backed by client, or nil when there
// is no Redis client.
func NewStreamDeny(client *redis.Client) *StreamDeny {
	if client == nil {
		return nil
	}
	return newStreamDeny(client, time.Now)
}

func newStreamDeny(client streamDenyClient, now func() time.Time) *StreamDeny {
	return &StreamDeny{client: client, now: now, cache: make(map[string]streamDenyEntry)}
}

// StreamDenyKey is the Redis key that marks one session denied.
func StreamDenyKey(sessionID string) string {
	return streamDenyKeyPrefix + sessionID
}

// Deny marks sessionID denied for MaxTokenTTL. The local cache is updated
// first so this replica refuses the session immediately even if the Redis
// write fails; the write failure is logged and otherwise ignored, because the
// session is already stopped and nothing useful can be done with the error.
func (d *StreamDeny) Deny(ctx context.Context, sessionID string) {
	if d == nil || sessionID == "" {
		return
	}
	d.remember(sessionID, true)
	if err := d.client.Set(ctx, StreamDenyKey(sessionID), streamDenyValue, MaxTokenTTL).Err(); err != nil {
		if d.shouldWarn() {
			d.logger().WarnContext(ctx, "stream deny marker write failed; session may keep serving until its tokens expire",
				"component", "playback", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
	}
}

// Denied reports whether sessionID carries a deny marker. Results are cached
// in-process for streamDenyCacheTTL. A Redis error fails open: the session is
// reported as not denied and the failure is logged at most once a minute.
func (d *StreamDeny) Denied(ctx context.Context, sessionID string) bool {
	if d == nil || sessionID == "" {
		return false
	}
	if denied, ok := d.cached(sessionID); ok {
		return denied
	}
	lookupCtx, cancel := context.WithTimeout(ctx, streamDenyLookupTimeout)
	defer cancel()
	err := d.client.Get(lookupCtx, StreamDenyKey(sessionID)).Err()
	denied := false
	switch {
	case err == nil:
		// Presence is the marker; the value only documents intent.
		denied = true
	case errors.Is(err, redis.Nil):
	default:
		if d.shouldWarn() {
			d.logger().WarnContext(ctx, "stream deny marker lookup failed; failing open",
				"component", "playback", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
	}
	// A failed lookup is cached too, so a broken Redis costs one attempt per
	// session per window instead of one per request.
	d.remember(sessionID, denied)
	return denied
}

func (d *StreamDeny) cached(sessionID string) (denied, ok bool) {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.cache[sessionID]
	if !ok {
		return false, false
	}
	if !now.Before(entry.expires) {
		delete(d.cache, sessionID)
		return false, false
	}
	return entry.denied, true
}

func (d *StreamDeny) remember(sessionID string, denied bool) {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.cache) >= streamDenyCacheSweepAt && !now.Before(d.nextSweep) {
		for id, entry := range d.cache {
			if !now.Before(entry.expires) {
				delete(d.cache, id)
			}
		}
		d.nextSweep = now.Add(streamDenyCacheTTL)
	}
	d.cache[sessionID] = streamDenyEntry{denied: denied, expires: now.Add(streamDenyCacheTTL)}
}

// shouldWarn admits one warning per streamDenyWarnEvery across both paths.
func (d *StreamDeny) shouldWarn() bool {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.lastWarn.IsZero() && now.Sub(d.lastWarn) < streamDenyWarnEvery {
		return false
	}
	d.lastWarn = now
	return true
}

func (d *StreamDeny) logger() *slog.Logger {
	if d.log != nil {
		return d.log
	}
	return slog.Default()
}
