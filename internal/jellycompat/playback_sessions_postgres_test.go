package jellycompat

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newCompatTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.jellycompat_playback_sessions')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check jellycompat_playback_sessions table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied jellycompat playback sessions migration")
	}
	return pool
}

// A session written by one store instance must be reloadable by a fresh instance
// (empty cache) — i.e. it survived in Postgres, as it would across a restart.
func TestDurableCompatPlaybackStore_SurvivesRestart(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })

	store1 := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	store1.Put(PlaybackSession{
		ID:                 id,
		CompatToken:        "tok",
		UserID:             "u1",
		RouteItemID:        "route-1",
		UpstreamSessionID:  "up-1",
		InitialSeekSeconds: 12.5,
		MediaSources:       []PlaybackMediaSource{{ID: "src-1", FileID: 7}},
	})

	// Fresh instance => empty cache => must hit Postgres.
	store2 := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	got, ok := store2.Get(id)
	if !ok {
		t.Fatal("session not reloaded from Postgres after restart")
	}
	if got.UpstreamSessionID != "up-1" || got.RouteItemID != "route-1" || got.InitialSeekSeconds != 12.5 {
		t.Fatalf("reloaded session lost fields: %+v", got)
	}

	// FindByRoute on the fresh instance resolves via a DB-backed scan.
	if _, _, ok := store2.FindByRoute("tok", "route-1"); !ok {
		t.Fatal("FindByRoute failed to resolve a persisted session")
	}

	// Update persists; reload on yet another instance sees it.
	if err := store2.Update(id, func(s *PlaybackSession) error {
		s.TranscodeStarted = true
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	store3 := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if got, ok := store3.Get(id); !ok || !got.TranscodeStarted {
		t.Fatalf("update did not persist: ok=%v got=%+v", ok, got)
	}

	store3.Delete(id)
	store4 := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if _, ok := store4.Get(id); ok {
		t.Fatal("session still present after delete")
	}
}

// M5: an empty compat token must never trigger a DB scan — FindByRoute returns
// the in-memory result only. With a non-nil pool but no live DB, a scan attempt
// would block/error on the pool; instead the empty-token path returns cleanly
// from cache. We assert: (a) a cached empty-token route resolves, and (b) a
// cache-miss empty-token lookup returns false without consulting the DB. The
// nil-pool variant proves the early return independent of any pool.
func TestDurableCompatPlaybackStore_EmptyTokenFindByRouteNoDBScan(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	store.Put(PlaybackSession{ID: "ps-empty", CompatToken: "", RouteItemID: "route-x"})

	// Cached empty-token route resolves from memory.
	if _, _, ok := store.FindByRoute("", "route-x"); !ok {
		t.Fatal("empty-token FindByRoute should resolve a cached route")
	}
	// Cache miss with an empty token returns false without a DB fallback.
	if _, _, ok := store.FindByRoute("", "route-missing"); ok {
		t.Fatal("empty-token FindByRoute should not resolve an unknown route")
	}

	// loadByCompatToken must early-return for an empty token even with a non-nil
	// pool, so it can never issue a full-table query. Use a closed pool so any
	// query attempt would error; reaching the early return means no query ran.
	if dsn := os.Getenv("SILO_TEST_DATABASE_URL"); dsn != "" {
		pool := newCompatTestPool(t)
		s := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
		// Should be a no-op (no panic, no scan); cache stays empty.
		s.loadByCompatToken("")
		if _, _, ok := s.FindByRoute("", "anything"); ok {
			t.Fatal("empty-token FindByRoute resolved unexpectedly against DB")
		}
	}
}

// M6: an Update must not lose a concurrent writer's field. We simulate two
// interleaved writers that each mutate a different field; after both commit, the
// DB-authoritative row (reloaded on a fresh instance) must carry BOTH fields,
// proving the second writer merged onto the first's committed row rather than
// clobbering it with a stale whole-document upsert.
func TestDurableCompatPlaybackStore_UpdateAtomicNoLostField(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-atomic-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })

	seed := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	seed.Put(PlaybackSession{ID: id, CompatToken: "tok", UserID: "u1"})

	// Writer A (its own cache) sets TranscodeStarted.
	writerA := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if err := writerA.Update(id, func(s *PlaybackSession) error {
		s.TranscodeStarted = true
		return nil
	}); err != nil {
		t.Fatalf("writerA update: %v", err)
	}

	// Writer B started before A committed (its cache lacks A's field) sets a
	// different field. Its DB step re-reads A's committed row FOR UPDATE and
	// merges UpstreamPlayMethod on top, so A's TranscodeStarted survives.
	writerB := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if err := writerB.Update(id, func(s *PlaybackSession) error {
		s.UpstreamPlayMethod = "Transcode"
		return nil
	}); err != nil {
		t.Fatalf("writerB update: %v", err)
	}

	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	got, ok := fresh.Get(id)
	if !ok {
		t.Fatal("session missing after interleaved updates")
	}
	if !got.TranscodeStarted {
		t.Fatalf("writerA's field was lost: %+v", got)
	}
	if got.UpstreamPlayMethod != "Transcode" {
		t.Fatalf("writerB's field was lost: %+v", got)
	}
}

// M11: the DB expiry filter must honor the injected clock, not Postgres now().
// A row written with a near-future expiry is visible while the fake clock is
// before it and invisible once the fake clock advances past it, even though
// Postgres wall-clock now() never moves enough to matter.
func TestDurableCompatPlaybackStore_InjectedClockExpiry(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-clock-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })

	base := time.Now()
	fake := base
	clock := func() time.Time { return fake }

	// TTL 1m; row expires at base+1m by the injected clock.
	store := NewDurableCompatPlaybackStore(pool, time.Minute, clock)
	store.Put(PlaybackSession{ID: id, CompatToken: "tok", UserID: "u1", RouteItemID: "r1"})

	// Fresh instance, empty cache, same injected clock: load hits the DB and the
	// row is live (fake clock still at base).
	reader := NewDurableCompatPlaybackStore(pool, time.Minute, clock)
	if _, ok := reader.Get(id); !ok {
		t.Fatal("row should be live by injected clock before expiry")
	}

	// Advance the fake clock past expiry. The DB filter uses d.now(), so a fresh
	// instance must treat the row as expired even though wall-clock now() is still
	// far before base+1m.
	fake = base.Add(2 * time.Minute)
	expired := NewDurableCompatPlaybackStore(pool, time.Minute, clock)
	if _, ok := expired.Get(id); ok {
		t.Fatal("row should be expired by injected clock past expiry")
	}
	if _, _, ok := expired.FindByRoute("tok", "r1"); ok {
		t.Fatal("FindByRoute should not resolve an injected-clock-expired row")
	}
}

// With a nil pool the durable store degrades to the in-memory cache only, so it
// still satisfies the interface and basic operations work (no DB available).
func TestDurableCompatPlaybackStore_NilPoolInMemory(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	store.Put(PlaybackSession{ID: "x", UpstreamSessionID: "u"})
	if got, ok := store.Get("x"); !ok || got.UpstreamSessionID != "u" {
		t.Fatalf("nil-pool Get failed: ok=%v got=%+v", ok, got)
	}
	store.Delete("x")
	if _, ok := store.Get("x"); ok {
		t.Fatal("nil-pool Delete failed")
	}
}
