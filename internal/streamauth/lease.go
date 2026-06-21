// Package streamauth implements the session-deny half of TR-lease: an admin
// Stop/Terminate writes a per-session "deny" marker to Redis, and the offload
// nodes read it on the serve path and enforce it by withholding bytes. This is
// the one revocation case that the offloaded topology cannot otherwise cover —
// a node-served direct/remux stream has no producer to kill and is authorized by
// a still-valid 24h signed token, so without this marker an admin "kill this
// stream" would not take effect until the token expired.
//
// Scope is deliberately narrow. New playback is already blocked at central
// (`/playback/start` is RequireAuth), and a cooperative client is stopped over
// the realtime WebSocket. Only the explicit, session-scoped admin kill of a
// non-cooperative node-served stream needs this. Passive bans / partial access
// changes are NOT enforced here: they take effect at the next play and are
// otherwise bounded by the token TTL — see the architecture doc's residuals.
package streamauth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

// KeyPrefix namespaces per-session deny keys: silo:streamauth:<sid>.
const KeyPrefix = "silo:streamauth:"

// DefaultDenyTTL bounds how long a deny marker survives. It must be ≥ the
// stream-token lifetime: a denied session never receives a fresh token, so once
// every token minted before the deny has expired the marker can lapse
// harmlessly. It matches playback.MaxTokenTTL (24h).
const DefaultDenyTTL = 24 * time.Hour

// allowedTimeout bounds the per-request Redis GET in Allowed. Allowed sits on
// the byte path of every segment/manifest/direct/remux request, so a degraded
// Redis must not add unbounded blocking latency to first-byte. On timeout we
// fail open (serve), matching the absent-key semantics.
const allowedTimeout = 500 * time.Millisecond

// allowedCacheTTL is how long a positive ("allowed") Allowed result is cached
// per session, so segment-heavy playback issues one Redis GET per session per
// window instead of one per segment. The TR-lease design explicitly tolerates a
// few seconds of revocation latency (a denied session is otherwise bounded by
// the token TTL), so caching the common allow path for this long is safe. Deny
// results are never cached as allowed — a deny takes effect on the next request.
const allowedCacheTTL = 3 * time.Second

// allowedCacheMax bounds the in-process allow cache so a flood of distinct
// session IDs cannot grow it without limit. When the cap is reached, cacheAllow
// reclaims room with graceful eviction (expired entries first, then the
// soonest-to-expire) rather than wiping the whole map — see cacheAllow.
const allowedCacheMax = 4096

// allowedCacheEvictTarget is how many soonest-to-expire entries cacheAllow drops
// in a single pass when expiring stale entries did not reclaim enough room. It is
// small so a steady stream of distinct sessions amortizes the eviction cost
// across many inserts instead of paying a wholesale wipe at the cap.
const allowedCacheEvictTarget = 64

// degradedFailOpen counts Allowed calls that failed open because of a Redis
// transport error (not the normal absent-key path), so operators can alert on
// revocation enforcement being silently degraded.
var degradedFailOpen = promauto.NewCounter(prometheus.CounterOpts{
	Name: "streamapp_streamauth_fail_open_degraded_total",
	Help: "Total Allowed() calls that served bytes by failing open due to a Redis error (revocation enforcement degraded).",
})

// Store is the Redis-backed deny store shared by central (writer, on admin kill)
// and the offload nodes (reader, on every serve).
type Store struct {
	rdb *redis.Client
	ttl time.Duration

	// allowCache memoizes positive Allowed results per session for
	// allowedCacheTTL, bounded by allowedCacheMax. It lives in the Store so any
	// caller (the proxy and the integrated server) shares the per-session
	// Redis-load reduction.
	mu         sync.Mutex
	allowCache map[string]time.Time // sessionID → cached-allow expiry

	// degradedLogMu rate-limits the fail-open-due-to-error log to one line per
	// degradedLogEvery, so a fully-down Redis logs steadily instead of once per
	// request.
	degradedLogMu   sync.Mutex
	degradedLogNext time.Time
}

// degradedLogEvery bounds how often the fail-open-due-to-error case logs.
const degradedLogEvery = 5 * time.Second

// NewStore wraps a Redis client. A nil client yields a disabled store whose
// reads fail open and whose writes no-op, so a single integrated box (no Redis)
// needs no special-casing.
func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultDenyTTL
	}
	return &Store{rdb: rdb, ttl: ttl, allowCache: make(map[string]time.Time)}
}

func key(sessionID string) string { return KeyPrefix + sessionID }

// Deny marks a session revoked so the nodes refuse it on the next request. It
// also drops any local cached-allow entry on this process so a same-process
// reader (e.g. the integrated server, which both denies and serves) honors the
// deny immediately rather than within allowedCacheTTL.
func (s *Store) Deny(ctx context.Context, sessionID string) error {
	if s == nil || s.rdb == nil || sessionID == "" {
		return nil
	}
	err := s.rdb.Set(ctx, key(sessionID), "deny", s.ttl).Err()
	if err == nil {
		s.mu.Lock()
		delete(s.allowCache, sessionID)
		s.mu.Unlock()
	}
	return err
}

// Allowed reports whether a node should serve sessionID. It fails OPEN: only a
// present deny marker withholds bytes; an absent key (the normal case) or a
// Redis error serves, preferring availability over revocation latency.
//
// A positive result is cached per session for allowedCacheTTL so segment-heavy
// playback collapses to one Redis GET per session per window. The Redis GET is
// bounded by allowedTimeout so a degraded Redis cannot add unbounded latency to
// the byte path; a timeout (or any other transport error) fails open and is
// counted/logged so operators see that revocation is degraded.
func (s *Store) Allowed(ctx context.Context, sessionID string) bool {
	if s == nil || s.rdb == nil || sessionID == "" {
		return true
	}

	now := time.Now()
	s.mu.Lock()
	if exp, ok := s.allowCache[sessionID]; ok && now.Before(exp) {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	getCtx, cancel := context.WithTimeout(ctx, allowedTimeout)
	defer cancel()

	_, err := s.rdb.Get(getCtx, key(sessionID)).Result()
	switch {
	case err == nil:
		return false // any present key is a deny — never cached as allowed
	case errors.Is(err, redis.Nil):
		s.cacheAllow(sessionID, now) // normal absent-key path
		return true
	default:
		// Transport error or timeout: fail open, but make it observable so an
		// operator can tell revocation enforcement is currently degraded.
		degradedFailOpen.Inc()
		s.logDegraded(err)
		return true // not cached: retry Redis on the next request
	}
}

// cacheAllow records that sessionID was observed allowed at now, keeping the
// cache bounded by allowedCacheMax with graceful eviction.
//
// Eviction policy (only runs when inserting a NEW key at/over the cap; updating
// an existing session's expiry never grows the map and so never evicts):
//  1. First drop already-EXPIRED entries — their allow-deadline has passed, so
//     they would fail the now.Before(exp) check in Allowed anyway and are free to
//     remove. Under churn this alone usually reclaims room.
//  2. If still at/over the cap, drop up to allowedCacheEvictTarget of the
//     SOONEST-TO-EXPIRE entries — the ones closest to lapsing on their own — so a
//     sustained flood of distinct, still-valid sessions makes room without
//     wiping unrelated live entries wholesale.
//
// This never wipes the whole map, so a still-valid entry is only evicted once it
// is among the nearest-expiry survivors, never merely because some unrelated
// session crossed the cap while reclaimable (expired) entries existed. Worst-case
// cost per evicting insert is O(n): one pass to delete expired entries, then at
// most allowedCacheEvictTarget selection passes over the (now smaller) map to
// pick the soonest-to-expire victims. Each pass is bounded and allocation-light
// (no sort, no slice retained), so the per-insert work stays bounded.
func (s *Store) cacheAllow(sessionID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Updating an existing session never grows the map; only a genuinely new key
	// at the cap needs to make room.
	if _, exists := s.allowCache[sessionID]; !exists && len(s.allowCache) >= allowedCacheMax {
		s.evictLocked(now)
	}
	s.allowCache[sessionID] = now.Add(allowedCacheTTL)
}

// evictLocked frees room in allowCache per cacheAllow's policy. Caller holds s.mu.
func (s *Store) evictLocked(now time.Time) {
	// Step 1: reclaim already-expired entries — free to drop.
	for sid, exp := range s.allowCache {
		if !now.Before(exp) {
			delete(s.allowCache, sid)
		}
	}

	// Step 2: if still at/over the cap, drop up to allowedCacheEvictTarget of the
	// soonest-to-expire entries. Each iteration scans for the current minimum
	// expiry and removes it; bounded by allowedCacheEvictTarget passes.
	for dropped := 0; dropped < allowedCacheEvictTarget && len(s.allowCache) >= allowedCacheMax; dropped++ {
		var (
			minSID string
			minExp time.Time
			found  bool
		)
		for sid, exp := range s.allowCache {
			if !found || exp.Before(minExp) {
				minSID, minExp, found = sid, exp, true
			}
		}
		if !found {
			break
		}
		delete(s.allowCache, minSID)
	}
}

// logDegraded emits the fail-open-due-to-error signal at most once per
// degradedLogEvery so a fully-down Redis logs steadily rather than per request.
func (s *Store) logDegraded(err error) {
	now := time.Now()
	s.degradedLogMu.Lock()
	if now.Before(s.degradedLogNext) {
		s.degradedLogMu.Unlock()
		return
	}
	s.degradedLogNext = now.Add(degradedLogEvery)
	s.degradedLogMu.Unlock()
	slog.Warn("streamauth deny lookup failed; serving (revocation enforcement degraded)", "error", err)
}
