package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeStreamDenyClient stands in for the go-redis client: a key map with the
// TTL each SET carried, an injectable error, and call counts so tests can see
// when the cache short-circuits Redis.
type fakeStreamDenyClient struct {
	mu   sync.Mutex
	keys map[string]fakeStreamDenyKey
	err  error
	gets int
	sets int
}

type fakeStreamDenyKey struct {
	value string
	ttl   time.Duration
}

func newFakeStreamDenyClient() *fakeStreamDenyClient {
	return &fakeStreamDenyClient{keys: make(map[string]fakeStreamDenyKey)}
}

func (f *fakeStreamDenyClient) Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if f.err != nil {
		cmd.SetErr(f.err)
		return cmd
	}
	f.keys[key] = fakeStreamDenyKey{value: fmt.Sprint(value), ttl: expiration}
	cmd.SetVal("OK")
	return cmd
}

func (f *fakeStreamDenyClient) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.err != nil {
		cmd.SetErr(f.err)
		return cmd
	}
	entry, ok := f.keys[key]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(entry.value)
	return cmd
}

func (f *fakeStreamDenyClient) counts() (gets, sets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets, f.sets
}

func (f *fakeStreamDenyClient) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type streamDenyTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *streamDenyTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *streamDenyTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// countingHandler records how many warnings the marker emitted.
type countingHandler struct {
	slog.Handler
	mu    sync.Mutex
	count int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.count++
	h.mu.Unlock()
	return h.Handler.Handle(ctx, r)
}

func (h *countingHandler) warnings() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func newTestStreamDeny(t *testing.T) (*StreamDeny, *fakeStreamDenyClient, *streamDenyTestClock, *countingHandler) {
	t.Helper()
	client := newFakeStreamDenyClient()
	clock := &streamDenyTestClock{now: time.Unix(1_700_000_000, 0)}
	handler := &countingHandler{Handler: slog.DiscardHandler}
	deny := newStreamDeny(client, clock.Now)
	deny.log = slog.New(handler)
	return deny, client, clock, handler
}

func TestStreamDenyNilIsNoOp(t *testing.T) {
	var deny *StreamDeny
	deny.Deny(t.Context(), "session")
	if deny.Denied(t.Context(), "session") {
		t.Fatal("nil StreamDeny reported a session denied")
	}
	if NewStreamDeny(nil) != nil {
		t.Fatal("NewStreamDeny(nil) must return nil so callers can keep the no-op path")
	}
}

func TestStreamDenyWritesMarkerWithTokenLifetime(t *testing.T) {
	deny, client, _, _ := newTestStreamDeny(t)
	deny.Deny(t.Context(), "sid-1")
	key, ok := client.keys["silo:streamauth:sid-1"]
	if !ok {
		t.Fatalf("marker key missing; keys = %v", client.keys)
	}
	if key.value != "deny" || key.ttl != MaxTokenTTL {
		t.Fatalf("marker = %+v, want value deny with TTL %s", key, MaxTokenTTL)
	}
	if !deny.Denied(t.Context(), "sid-1") {
		t.Fatal("denied session reported as allowed")
	}
	if deny.Denied(t.Context(), "sid-2") {
		t.Fatal("unrelated session reported as denied")
	}
	if deny.Denied(t.Context(), "") {
		t.Fatal("empty session id reported as denied")
	}
}

func TestStreamDenyCachesLookupsPerSession(t *testing.T) {
	deny, client, clock, _ := newTestStreamDeny(t)
	for range 3 {
		if deny.Denied(t.Context(), "sid-1") {
			t.Fatal("unmarked session reported as denied")
		}
	}
	if gets, _ := client.counts(); gets != 1 {
		t.Fatalf("gets = %d, want one lookup within the cache window", gets)
	}
	// Another replica writes the marker; this replica keeps its cached
	// answer until the window elapses, then sees the marker.
	client.keys[StreamDenyKey("sid-1")] = fakeStreamDenyKey{value: "deny", ttl: MaxTokenTTL}
	if deny.Denied(t.Context(), "sid-1") {
		t.Fatal("cached answer changed before the window elapsed")
	}
	clock.Advance(streamDenyCacheTTL)
	if !deny.Denied(t.Context(), "sid-1") {
		t.Fatal("marker not seen after the cache window elapsed")
	}
	if gets, _ := client.counts(); gets != 2 {
		t.Fatalf("gets = %d, want a second lookup after expiry", gets)
	}
	// Sessions are cached independently.
	if deny.Denied(t.Context(), "sid-2") {
		t.Fatal("unrelated session reported as denied")
	}
	if gets, _ := client.counts(); gets != 3 {
		t.Fatalf("gets = %d, want a lookup for the second session", gets)
	}
}

func TestStreamDenyLocalDenyIsVisibleWithoutLookup(t *testing.T) {
	deny, client, _, _ := newTestStreamDeny(t)
	deny.Deny(t.Context(), "sid-1")
	if !deny.Denied(t.Context(), "sid-1") {
		t.Fatal("local deny not visible")
	}
	if gets, sets := client.counts(); gets != 0 || sets != 1 {
		t.Fatalf("gets = %d, sets = %d; want the deny to prime the cache with no lookup", gets, sets)
	}
}

func TestStreamDenyFailsOpenAndRateLimitsWarnings(t *testing.T) {
	deny, client, clock, handler := newTestStreamDeny(t)
	client.setErr(errors.New("redis down"))

	if deny.Denied(t.Context(), "sid-1") {
		t.Fatal("Redis error must fail open")
	}
	if deny.Denied(t.Context(), "sid-2") {
		t.Fatal("Redis error must fail open")
	}
	if handler.warnings() != 1 {
		t.Fatalf("warnings = %d, want one per minute", handler.warnings())
	}
	// The failed lookup is cached for the window so a broken Redis is not
	// hit on every request.
	deny.Denied(t.Context(), "sid-1")
	if gets, _ := client.counts(); gets != 2 {
		t.Fatalf("gets = %d, want failed lookups cached per session", gets)
	}

	clock.Advance(streamDenyWarnEvery)
	deny.Denied(t.Context(), "sid-1")
	if handler.warnings() != 2 {
		t.Fatalf("warnings = %d, want a second warning after a minute", handler.warnings())
	}

	// A failed write still denies locally, and shares the warning budget.
	deny.Deny(t.Context(), "sid-3")
	if !deny.Denied(t.Context(), "sid-3") {
		t.Fatal("local deny lost when the Redis write failed")
	}
	if handler.warnings() != 2 {
		t.Fatalf("warnings = %d, want the write failure rate limited with lookups", handler.warnings())
	}

	// Redis recovers: after the cache window, lookups resume and see the truth.
	client.setErr(nil)
	client.keys[StreamDenyKey("sid-1")] = fakeStreamDenyKey{value: "deny", ttl: MaxTokenTTL}
	clock.Advance(streamDenyCacheTTL)
	if !deny.Denied(t.Context(), "sid-1") {
		t.Fatal("marker not seen after Redis recovered")
	}
}

func TestStreamDenySweepsExpiredCacheEntries(t *testing.T) {
	deny, _, clock, _ := newTestStreamDeny(t)
	for i := range streamDenyCacheSweepAt {
		deny.Denied(t.Context(), fmt.Sprintf("sid-%d", i))
	}
	clock.Advance(streamDenyCacheTTL)
	deny.Denied(t.Context(), "fresh")
	deny.mu.Lock()
	size := len(deny.cache)
	deny.mu.Unlock()
	if size != 1 {
		t.Fatalf("cache size = %d, want expired entries swept on insert", size)
	}
}
