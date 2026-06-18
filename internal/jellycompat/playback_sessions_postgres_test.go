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
