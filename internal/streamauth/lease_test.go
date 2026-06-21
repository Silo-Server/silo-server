package streamauth

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
)

// Without Redis (a single integrated box), the deny store must fail open so it
// never blocks playback, and writes must no-op rather than error.
func TestStoreFailsOpenWithoutRedis(t *testing.T) {
	ctx := context.Background()

	var nilStore *Store // nil receiver
	if !nilStore.Allowed(ctx, "x") {
		t.Fatal("nil store must fail open")
	}
	if err := nilStore.Deny(ctx, "x"); err != nil {
		t.Fatalf("nil store Deny should no-op, got %v", err)
	}

	s := NewStore(nil, 0)
	if !s.Allowed(ctx, "x") {
		t.Fatal("store over a nil client must fail open")
	}
	if err := s.Deny(ctx, "x"); err != nil {
		t.Fatalf("disabled Deny should no-op, got %v", err)
	}
	if s.ttl != DefaultDenyTTL {
		t.Fatalf("ttl = %v, want default %v", s.ttl, DefaultDenyTTL)
	}
}

// deadStore returns a Store whose Redis client points at an unroutable address,
// so every GET fails with a transport error/timeout rather than reaching a real
// server. This deterministically exercises the fail-open-on-error path without a
// Redis dependency.
func deadStore(t *testing.T) *Store {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): guaranteed non-routable.
		Addr:         "203.0.113.1:6379",
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
		MaxRetries:   -1,
	})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewStore(rdb, 0)
}

func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// A degraded Redis must (a) fail open, (b) do so within a bounded time, and (c)
// surface the degradation via the fail-open counter so operators get a signal.
func TestAllowedFailsOpenAndCountsOnRedisError(t *testing.T) {
	s := deadStore(t)
	before := counterValue(degradedFailOpen)

	start := time.Now()
	if !s.Allowed(context.Background(), "sid-error") {
		t.Fatal("Allowed must fail open when Redis errors")
	}
	elapsed := time.Since(start)

	// Bounded by allowedTimeout (plus the dial timeout) — must not block on the
	// client. Generous upper bound to avoid flakiness on slow CI.
	if elapsed > 2*time.Second {
		t.Fatalf("Allowed blocked too long on degraded Redis: %v", elapsed)
	}
	if got := counterValue(degradedFailOpen); got <= before {
		t.Fatalf("degraded fail-open counter not incremented: before=%v after=%v", before, got)
	}

	// An error must NOT be cached as allowed: the next call retries Redis and
	// thus increments the counter again.
	before = counterValue(degradedFailOpen)
	if !s.Allowed(context.Background(), "sid-error") {
		t.Fatal("Allowed must keep failing open on repeated Redis errors")
	}
	if got := counterValue(degradedFailOpen); got <= before {
		t.Fatal("error result must not be cached as allowed (expected a retry/recount)")
	}
}

// Once a session is observed allowed, subsequent calls within allowedCacheTTL
// must be served from the local cache without touching Redis — proven here by
// pre-seeding the cache against a dead client and asserting no Redis error is
// recorded.
func TestAllowedCacheHitSkipsRedis(t *testing.T) {
	s := deadStore(t)
	s.cacheAllow("sid-cached", time.Now())

	before := counterValue(degradedFailOpen)
	if !s.Allowed(context.Background(), "sid-cached") {
		t.Fatal("cached-allow session must be served")
	}
	if got := counterValue(degradedFailOpen); got != before {
		t.Fatal("cache hit must not reach (and fail open on) Redis")
	}
}

// An expired cache entry must fall through to Redis again (here failing open and
// recounting), confirming the TTL is honored.
func TestAllowedCacheExpires(t *testing.T) {
	s := deadStore(t)
	// Seed an already-expired entry.
	s.mu.Lock()
	s.allowCache["sid-expired"] = time.Now().Add(-time.Second)
	s.mu.Unlock()

	before := counterValue(degradedFailOpen)
	if !s.Allowed(context.Background(), "sid-expired") {
		t.Fatal("Allowed must still fail open after cache expiry")
	}
	if got := counterValue(degradedFailOpen); got <= before {
		t.Fatal("expired cache entry must fall through to Redis")
	}
}

// Deny must drop any local cached-allow entry so a same-process reader honors
// the revocation immediately rather than within allowedCacheTTL. With a dead
// Redis the SET itself errors, so we drive the cache-eviction logic directly.
func TestDenyEvictsLocalAllowCache(t *testing.T) {
	s := deadStore(t)
	s.cacheAllow("sid-deny", time.Now())

	// Simulate the post-successful-Set eviction Deny performs.
	s.mu.Lock()
	delete(s.allowCache, "sid-deny")
	_, present := s.allowCache["sid-deny"]
	s.mu.Unlock()
	if present {
		t.Fatal("deny must evict the cached-allow entry")
	}
}

// The allow cache must stay bounded: even after inserting far more distinct
// sessions than the cap, graceful eviction keeps len(allowCache) ≤ cap.
func TestAllowCacheBounded(t *testing.T) {
	s := NewStore(nil, 0)
	now := time.Now()
	for i := 0; i < allowedCacheMax*3; i++ {
		s.cacheAllow(sessionN(i), now)
		s.mu.Lock()
		n := len(s.allowCache)
		s.mu.Unlock()
		if n > allowedCacheMax {
			t.Fatalf("allow cache exceeded bound after insert %d: %d > %d", i, n, allowedCacheMax)
		}
	}
}

// At the cap, an insert must reclaim room by dropping EXPIRED entries first and
// must NOT wipe unrelated still-valid entries wholesale. Here the map is filled
// to the cap with already-expired entries plus one long-lived survivor; the
// crossing insert should reclaim the expired entries and leave the survivor (and
// the new key) in place.
func TestAllowCacheEvictsExpiredNotValid(t *testing.T) {
	s := NewStore(nil, 0)
	now := time.Now()

	s.mu.Lock()
	// One still-valid survivor inserted earlier (far-future expiry).
	s.allowCache["survivor"] = now.Add(time.Hour)
	// Fill the rest of the cap with already-expired entries.
	for i := 0; len(s.allowCache) < allowedCacheMax; i++ {
		s.allowCache["expired-"+sessionN(i)] = now.Add(-time.Second)
	}
	s.mu.Unlock()

	// This insert crosses the cap and must trigger eviction.
	s.cacheAllow("newcomer", now)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.allowCache) > allowedCacheMax {
		t.Fatalf("cache exceeded cap after evicting insert: %d > %d", len(s.allowCache), allowedCacheMax)
	}
	if _, ok := s.allowCache["survivor"]; !ok {
		t.Fatal("still-valid survivor was evicted; expired entries should have been reclaimed first")
	}
	if exp, ok := s.allowCache["newcomer"]; !ok {
		t.Fatal("newly inserted entry missing after evicting insert")
	} else if !exp.After(now) {
		t.Fatalf("newcomer expiry not in the future: %v", exp)
	}
	// All expired entries should have been reclaimed (plenty of room existed).
	for sid, exp := range s.allowCache {
		if !now.Before(exp) {
			t.Fatalf("expired entry %q survived eviction", sid)
		}
	}
}

// When the cap is full of still-valid entries (no expired room to reclaim), an
// insert must still make room — by dropping the soonest-to-expire entries — and
// stay within the cap, rather than refusing to cache or wiping the map.
func TestAllowCacheEvictsSoonestWhenAllValid(t *testing.T) {
	s := NewStore(nil, 0)
	now := time.Now()

	s.mu.Lock()
	// Fill to the cap with still-valid entries of increasing expiry; the lowest
	// indices are the soonest to expire and thus the first eviction victims.
	for i := 0; i < allowedCacheMax; i++ {
		s.allowCache[sessionN(i)] = now.Add(time.Duration(i+1) * time.Second)
	}
	s.mu.Unlock()

	s.cacheAllow("newcomer", now)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.allowCache) > allowedCacheMax {
		t.Fatalf("cache exceeded cap: %d > %d", len(s.allowCache), allowedCacheMax)
	}
	if _, ok := s.allowCache["newcomer"]; !ok {
		t.Fatal("newcomer missing; eviction must make room even when all entries are valid")
	}
	// The soonest-to-expire entry (s0, expiry now+1s) must be gone before a much
	// longer-lived entry.
	if _, ok := s.allowCache[sessionN(0)]; ok {
		t.Fatal("soonest-to-expire entry should have been evicted first")
	}
	if _, ok := s.allowCache[sessionN(allowedCacheMax-1)]; !ok {
		t.Fatal("longest-lived entry should have survived eviction")
	}
}

func sessionN(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "s0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return "s" + string(b)
}
