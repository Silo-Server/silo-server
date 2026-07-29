package jellycompat

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchsync"
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

func TestDurableCompatPlaybackStoreRevalidatesUnstartedNegotiationAcrossInstances(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	firstID := "compat-first-" + suffix
	secondID := "compat-second-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(
			ctx,
			`DELETE FROM jellycompat_playback_sessions WHERE id = ANY($1)`,
			[]string{firstID, secondID},
		)
	})
	first := PlaybackSession{
		ID:             firstID,
		CompatToken:    "owner-" + suffix,
		ClientDeviceID: "device-1",
		RouteItemID:    "route-1",
	}
	seed := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	seed.PutNegotiated(first)

	stale := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if _, ok := stale.Get(firstID); !ok {
		t.Fatal("first negotiation was not cached")
	}
	replacer := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	second := first
	second.ID = secondID
	replacer.PutNegotiated(second)

	if _, ok := stale.Get(firstID); ok {
		t.Fatal("superseded unstarted negotiation remained routable from another instance's cache")
	}
}

func TestDurableCompatPlaybackStorePutNegotiatedReplacesAcrossInstances(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	firstID := "compat-negotiated-first-" + suffix
	secondID := "compat-negotiated-second-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = ANY($1)`, []string{firstID, secondID})
	})

	first := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	first.PutNegotiated(PlaybackSession{
		ID:             firstID,
		CompatToken:    "negotiated-token-" + suffix,
		ClientDeviceID: "web-device",
		RouteItemID:    "route-1",
	})

	second := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	second.PutNegotiated(PlaybackSession{
		ID:             secondID,
		CompatToken:    "negotiated-token-" + suffix,
		ClientDeviceID: "web-device",
		RouteItemID:    "route-1",
	})

	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if _, ok := fresh.Get(firstID); ok {
		t.Fatal("superseded negotiation remained durable")
	}
	if _, ok := fresh.Get(secondID); !ok {
		t.Fatal("replacement negotiation was not durable")
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

func TestPlaybackSessionStoreTerminalClaimIsAtomicAndOwnerScoped(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "owner", UpstreamSessionID: "upstream-1"})
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}

	if _, err := store.StageTerminal("play-1", "other", event, true); err == nil {
		t.Fatal("foreign token staged terminal event")
	}
	if _, err := store.StageTerminal("play-1", "owner", event, true); err != nil {
		t.Fatal("owner failed to stage terminal event")
	}
	claimUntil := time.Now().Add(time.Minute)
	if _, err := store.ClaimTerminal("play-1", "other", claimUntil); err == nil {
		t.Fatal("foreign token claimed terminal event")
	}
	got, err := store.ClaimTerminal("play-1", "owner", claimUntil)
	if err != nil || got.UpstreamSessionID != "upstream-1" {
		t.Fatalf("owner claim failed: err=%v session=%+v", err, got)
	}
	if _, err := store.ClaimTerminal("play-1", "owner", claimUntil.Add(time.Minute)); err == nil {
		t.Fatal("terminal event was claimed twice")
	}
}

func TestPlaybackSessionStoreDeactivateRetainsOnlyFinalReportLookup(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                  "play-1",
		CompatToken:         "owner",
		ClientPlaySessionID: "client-play-1",
		UpstreamSessionID:   "upstream-1",
	})

	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	terminal, err := store.StageTerminal("play-1", "owner", event, true)
	if err != nil || !terminal.Terminal {
		t.Fatalf("terminal stage failed: err=%v session=%+v", err, terminal)
	}
	if _, ok := store.Get("play-1"); ok {
		t.Fatal("terminal session remained available to ordinary lookup")
	}
	if _, ok := store.FindByClientPlaySessionID("owner", "client-play-1"); ok {
		t.Fatal("terminal session remained available to ordinary alias lookup")
	}
	if _, ok := store.GetFinalizable("play-1", "owner"); !ok {
		t.Fatal("terminal session was unavailable to final report lookup")
	}
	if _, ok := store.FindFinalizableByClientPlaySessionID("owner", "client-play-1", "", ""); !ok {
		t.Fatal("terminal session was unavailable to final alias lookup")
	}
	claimUntil := time.Now().Add(time.Minute)
	claimed, err := store.ClaimTerminal("play-1", "owner", claimUntil)
	if err != nil || !claimed.Terminal {
		t.Fatalf("final report could not claim terminal session: err=%v session=%+v", err, claimed)
	}
	store.CompleteTerminal("play-1", "owner", claimUntil, claimed.TerminalClaimVersion)
	if _, ok := store.GetFinalizable("play-1", "owner"); ok {
		t.Fatal("authoritatively completed terminal session was retained")
	}
}

func TestPlaybackSessionStoreFinalAliasLookupDisambiguatesReusedAlias(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                  "old-play",
		CompatToken:         "owner",
		ClientPlaySessionID: "reused-client-play",
		RouteItemID:         "old-item",
		MediaSources:        []PlaybackMediaSource{{ID: "old-source"}},
		UpstreamSessionID:   "old-upstream",
	})
	if _, err := store.StageTerminal(
		"old-play",
		"owner",
		watchsync.ScrobbleEvent{PlaybackSessionID: "old-upstream"},
		false,
	); err != nil {
		t.Fatalf("stage old terminal play: %v", err)
	}
	store.Put(PlaybackSession{
		ID:                  "current-play",
		CompatToken:         "owner",
		ClientPlaySessionID: "reused-client-play",
		RouteItemID:         "current-item",
		MediaSources:        []PlaybackMediaSource{{ID: "current-source"}},
		UpstreamSessionID:   "current-upstream",
	})

	if _, ok := store.FindFinalizableByClientPlaySessionID(
		"owner", "reused-client-play", "", "",
	); ok {
		t.Fatal("unscoped reused alias unexpectedly selected an arbitrary play")
	}
	current, ok := store.FindFinalizableByClientPlaySessionID(
		"owner", "reused-client-play", "current-item", "current-source",
	)
	if !ok || current.ID != "current-play" {
		t.Fatalf("current report resolved to ok=%v session=%+v", ok, current)
	}
	old, ok := store.FindFinalizableByClientPlaySessionID(
		"owner", "reused-client-play", "old-item", "old-source",
	)
	if !ok || old.ID != "old-play" {
		t.Fatalf("late old report resolved to ok=%v session=%+v", ok, old)
	}
	oldByRoute, _, ok := store.FindFinalizableByRoute("owner", "old-source")
	if !ok || oldByRoute.ID != "old-play" {
		t.Fatalf("terminal route resolved to ok=%v session=%+v", ok, oldByRoute)
	}
}

func TestPlaybackSessionStoreExpiredTerminalLeaseCanBeReclaimed(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "owner"})
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if _, err := store.StageTerminal("play-1", "owner", event, true); err != nil {
		t.Fatalf("stage terminal: %v", err)
	}
	firstLease := now.Add(10 * time.Second)
	firstClaim, err := store.ClaimTerminal("play-1", "owner", firstLease)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	now = firstLease.Add(time.Microsecond)
	secondLease := now.Add(10 * time.Second)
	secondClaim, err := store.ClaimTerminal("play-1", "owner", secondLease)
	if err != nil {
		t.Fatalf("reclaim expired lease: %v", err)
	}
	store.CompleteTerminal("play-1", "owner", firstLease, firstClaim.TerminalClaimVersion)
	if _, ok := store.GetFinalizable("play-1", "owner"); !ok {
		t.Fatal("stale first lease completed the successor's terminal row")
	}
	store.CompleteTerminal("play-1", "owner", secondLease, secondClaim.TerminalClaimVersion)
	if _, ok := store.GetFinalizable("play-1", "owner"); ok {
		t.Fatal("successor lease did not complete terminal row")
	}
}

func TestPlaybackSessionStorePendingScanEvictsExpiredTerminal(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewPlaybackSessionStore(time.Minute, func() time.Time { return now })
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "owner"})
	if _, err := store.StageTerminal(
		"play-1", "owner", watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}, false,
	); err != nil {
		t.Fatalf("stage terminal: %v", err)
	}
	now = now.Add(2 * time.Minute)
	pending, err := store.ListPendingTerminals(context.Background(), 100)
	if err != nil {
		t.Fatalf("list pending terminals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expired pending terminals = %d, want 0", len(pending))
	}
	store.mu.RLock()
	remaining := len(store.sessions)
	store.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("expired terminal entries retained in memory = %d", remaining)
	}
}

func TestDurableCompatPlaybackStoreExpiryClearsFailureBookkeeping(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewDurableCompatPlaybackStore(nil, time.Minute, func() time.Time { return now })
	store.mem.Put(PlaybackSession{ID: "play-1", CompatToken: "owner"})
	store.markUnpersisted("play-1")
	store.appendPendingUpdate("play-1", "owner", func(*PlaybackSession) error { return nil })
	now = now.Add(2 * time.Minute)

	if _, err := store.DeleteExpired(context.Background()); err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if store.isUnpersisted("play-1") {
		t.Fatal("expired session retained its unpersisted marker")
	}
	if store.hasPendingUpdates("play-1") {
		t.Fatal("expired session retained pending update closures")
	}
}

func TestDurableCompatPlaybackStoreBoundsGenerationTombstones(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	before := store.idGenerationSnapshot("old-session")
	for i := 0; i <= compatValidationCacheLimit; i++ {
		store.bumpCacheGenerations(fmt.Sprintf("missing-%d", i), "")
	}
	store.generationMu.Lock()
	count := len(store.idGenerations)
	epoch := store.generationEpoch
	store.generationMu.Unlock()
	if count > compatValidationCacheLimit {
		t.Fatalf("generation tombstones = %d, limit = %d", count, compatValidationCacheLimit)
	}
	if epoch == 0 || before == store.idGenerationSnapshot("old-session") {
		t.Fatal("generation eviction did not invalidate an older captured stamp")
	}
}

func TestDurableCompatPlaybackStoreIDWriteDoesNotRefreshTokenSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := NewDurableCompatPlaybackStore(nil, time.Hour, func() time.Time { return now })
	store.markTokenValidated("owner")
	now = now.Add(4 * time.Second)
	store.markIDValidated("play-2")
	now = now.Add(2 * time.Second)
	if !store.shouldRevalidateToken("owner") {
		t.Fatal("single-row validation incorrectly extended the token-wide cache window")
	}
}

func TestPlaybackSessionStoreNewAuthoritativeEventSurvivesStaleCompletion(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "owner"})
	firstEvent := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1", PositionSeconds: 45}
	if _, err := store.StageTerminal("play-1", "owner", firstEvent, true); err != nil {
		t.Fatalf("stage first event: %v", err)
	}
	firstLease := time.Now().Add(time.Minute)
	firstClaim, err := store.ClaimTerminal("play-1", "owner", firstLease)
	if err != nil {
		t.Fatalf("claim first event: %v", err)
	}
	secondEvent := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1", PositionSeconds: 90}
	if _, err := store.StageTerminal("play-1", "owner", secondEvent, true); err != nil {
		t.Fatalf("stage replacement event: %v", err)
	}

	store.CompleteTerminal("play-1", "owner", firstLease, firstClaim.TerminalClaimVersion)
	store.ReleaseTerminalClaim("play-1", "owner", firstLease, firstClaim.TerminalClaimVersion, false)
	pending, ok := store.GetFinalizable("play-1", "owner")
	if !ok || pending.TerminalScrobbleEvent == nil || pending.TerminalScrobbleEvent.PositionSeconds != 90 {
		t.Fatalf("replacement event was lost: ok=%v session=%+v", ok, pending)
	}
	secondLease := firstLease.Add(time.Minute)
	secondClaim, err := store.ClaimTerminal("play-1", "owner", secondLease)
	if err != nil {
		t.Fatalf("claim replacement event: %v", err)
	}
	store.CompleteTerminal("play-1", "owner", secondLease, secondClaim.TerminalClaimVersion)
	if _, ok := store.GetFinalizable("play-1", "owner"); ok {
		t.Fatal("replacement event was not completed")
	}
}

func TestDurableCompatPlaybackStoreTerminalClaimAcrossInstances(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-take-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })

	seed := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	seed.Put(PlaybackSession{ID: id, CompatToken: "owner", UpstreamSessionID: "upstream-1"})
	skewedNow := time.Now().Add(-6 * time.Hour)
	first := NewDurableCompatPlaybackStore(pool, time.Hour, func() time.Time { return skewedNow })
	second := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if _, ok := first.Get(id); !ok {
		t.Fatal("first instance did not load session")
	}
	if _, ok := second.Get(id); !ok {
		t.Fatal("second instance did not load session")
	}
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if _, err := seed.StageTerminal(id, "owner", event, true); err != nil {
		t.Fatal("failed to stage durable terminal event")
	}

	var dbBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbBefore); err != nil {
		t.Fatalf("read database clock: %v", err)
	}
	claimUntil := skewedNow.Add(time.Minute)
	claimed, err := first.ClaimTerminal(id, "owner", claimUntil)
	if err != nil {
		t.Fatal("first instance did not claim session")
	}
	var dbAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbAfter); err != nil {
		t.Fatalf("read database clock after claim: %v", err)
	}
	if claimed.TerminalClaimUntil.Before(dbBefore.Add(55*time.Second)) ||
		claimed.TerminalClaimUntil.After(dbAfter.Add(65*time.Second)) {
		t.Fatalf(
			"claim deadline %s was not anchored to database clock interval [%s, %s]",
			claimed.TerminalClaimUntil,
			dbBefore.Add(55*time.Second),
			dbAfter.Add(65*time.Second),
		)
	}
	if _, err := second.ClaimTerminal(id, "owner", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("second instance claimed an already leased durable session")
	}
}

func TestDurableCompatPlaybackStoreTerminalStageSurvivesInstanceBoundary(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-deactivate-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })
	now := time.Now()
	clock := func() time.Time { return now }

	seed := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	seed.Put(PlaybackSession{
		ID:                  id,
		CompatToken:         "owner",
		ClientPlaySessionID: "client-play-1",
		UpstreamSessionID:   "upstream-1",
	})
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	if _, ok := fresh.Get(id); !ok {
		t.Fatal("fresh instance did not preload the active session")
	}
	if _, ok := fresh.FindByClientPlaySessionID("owner", "client-play-1"); !ok {
		t.Fatal("fresh instance did not preload the active alias")
	}
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if terminal, err := seed.StageTerminal(id, "owner", event, false); err != nil || !terminal.Terminal {
		t.Fatalf("durable terminal stage failed: err=%v session=%+v", err, terminal)
	}
	// Cross-process invalidation is deliberately bounded rather than putting a
	// DB query on every segment request. Advance past that window before the
	// other instance revalidates its preloaded cache.
	now = now.Add(compatCacheRevalidationInterval)

	if _, ok := fresh.Get(id); ok {
		t.Fatal("instance routed a terminal session from its stale active cache")
	}
	if _, ok := fresh.FindByClientPlaySessionID("owner", "client-play-1"); ok {
		t.Fatal("instance routed a terminal alias from its stale active cache")
	}
	if _, ok := fresh.GetFinalizable(id, "owner"); !ok {
		t.Fatal("fresh instance could not resolve terminal session for final report")
	}
	if _, ok := fresh.FindFinalizableByClientPlaySessionID("owner", "client-play-1", "", ""); !ok {
		t.Fatal("fresh instance could not resolve terminal alias for final report")
	}
	claimUntil := time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute)
	claimed, err := fresh.ClaimTerminal(id, "owner", claimUntil)
	if err != nil || !claimed.Terminal {
		t.Fatalf("fresh instance could not claim terminal session: err=%v session=%+v", err, claimed)
	}
	fresh.ReleaseTerminalClaim(id, "owner", claimed.TerminalClaimUntil, claimed.TerminalClaimVersion, true)
	if claimed, err := fresh.ClaimTerminal(id, "owner", claimUntil.Add(time.Minute)); err == nil || claimed != nil {
		t.Fatalf("delivered fallback was claimed again: err=%v session=%+v", err, claimed)
	}
}

func TestDurableCompatPlaybackStorePreservesCachedSessionOnValidationFailure(t *testing.T) {
	pool := newCompatTestPool(t)
	now := time.Now()
	clock := func() time.Time { return now }
	store := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	pool.Close()
	store.Put(PlaybackSession{ID: "cached-session", CompatToken: "owner"})

	// A request inside the validation window must be served entirely from cache.
	if _, ok := store.Get("cached-session"); !ok {
		t.Fatal("hot cache lookup consulted the closed database")
	}

	// Once validation is due, the failed query must not evict a last-known-good
	// active session and interrupt playback.
	now = now.Add(compatCacheRevalidationInterval)
	if _, ok := store.Get("cached-session"); !ok {
		t.Fatal("database validation failure evicted the cached active session")
	}
}

func TestDurableCompatPlaybackStoreRepairsFailedInitialPersistence(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-repair-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })
	now := time.Now()
	clock := func() time.Time { return now }
	store := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	stored := store.mem.putNormalized(PlaybackSession{ID: id, CompatToken: "owner", UpstreamSessionID: "upstream-1"})
	store.markIDValidated(id)
	store.markUnpersisted(id)

	now = now.Add(compatCacheRevalidationInterval)
	if got, ok := store.Get(id); !ok || got.UpstreamSessionID != "upstream-1" {
		t.Fatalf("unpersisted cache entry was not retained and repaired: ok=%v session=%+v", ok, got)
	}
	if store.isUnpersisted(id) {
		t.Fatal("successfully repaired session remained marked unpersisted")
	}
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	if got, ok := fresh.Get(stored.ID); !ok || got.UpstreamSessionID != "upstream-1" {
		t.Fatalf("repaired session was not durable: ok=%v session=%+v", ok, got)
	}
}

func TestDurableCompatPlaybackStorePreservesUnpersistedTerminalOnRevalidation(t *testing.T) {
	pool := newCompatTestPool(t)
	id := fmt.Sprintf("compat-unpersisted-terminal-%d", time.Now().UnixNano())
	now := time.Now()
	clock := func() time.Time { return now }
	store := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	store.mem.Put(PlaybackSession{ID: id, CompatToken: "owner"})
	if err := store.mem.HideFromRouting(id, "owner"); err != nil {
		t.Fatalf("hide terminal: %v", err)
	}
	store.markUnpersisted(id)
	store.markIDValidated(id)
	now = now.Add(compatCacheRevalidationInterval)

	if _, ok := store.Get(id); ok {
		t.Fatal("terminal session became routable during missing-row revalidation")
	}
	terminal, ok := store.GetFinalizable(id, "owner")
	if !ok || !terminal.Terminal || !store.isUnpersisted(id) {
		t.Fatalf("unpersisted terminal state was lost: ok=%v session=%+v", ok, terminal)
	}
}

func TestDurableCompatPlaybackStoreDiscardsTokenSnapshotAfterLocalMutation(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	generation := store.tokenGenerationSnapshot("owner")
	store.cacheMutationMu.Lock()
	store.mem.Put(PlaybackSession{ID: "new-session", CompatToken: "owner", RouteItemID: "route-1"})
	store.bumpCacheGenerations("new-session", "owner")
	store.cacheMutationMu.Unlock()

	if store.applyCompatTokenSnapshot("owner", nil, generation) {
		t.Fatal("stale token snapshot applied after a concurrent local mutation")
	}
	if _, _, ok := store.mem.FindByRoute("owner", "route-1"); !ok {
		t.Fatal("stale token snapshot deleted the concurrent local session")
	}
}

func TestDurableCompatPlaybackStoreAppliesTokenSnapshotAfterUnrelatedMutation(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	store.mem.Put(PlaybackSession{ID: "stale-session", CompatToken: "owner", RouteItemID: "route-1"})
	generation := store.tokenGenerationSnapshot("owner")
	store.cacheMutationMu.Lock()
	store.mem.Put(PlaybackSession{ID: "other-session", CompatToken: "other"})
	store.bumpCacheGenerations("other-session", "other")
	store.cacheMutationMu.Unlock()

	if !store.applyCompatTokenSnapshot("owner", nil, generation) {
		t.Fatal("unrelated local mutation incorrectly invalidated the token snapshot")
	}
	if _, _, ok := store.mem.FindByRoute("owner", "route-1"); ok {
		t.Fatal("valid token snapshot was not applied after unrelated mutation")
	}
}

func TestDurableCompatPlaybackStoreDoesNotReviveLocalTerminalFromActiveSnapshot(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	active := PlaybackSession{ID: "play-1", CompatToken: "owner", RouteItemID: "route-1"}
	store.mem.Put(active)
	if err := store.mem.HideFromRouting("play-1", "owner"); err != nil {
		t.Fatalf("hide local terminal: %v", err)
	}
	generation := store.tokenGenerationSnapshot("owner")

	if !store.applyCompatTokenSnapshot("owner", []PlaybackSession{active}, generation) {
		t.Fatal("active durable snapshot was unexpectedly discarded")
	}
	if _, ok := store.mem.Get("play-1"); ok {
		t.Fatal("active durable snapshot revived a locally terminal session")
	}
	if terminal, ok := store.mem.GetFinalizable("play-1", "owner"); !ok || !terminal.Terminal {
		t.Fatalf("local terminal marker was lost: ok=%v session=%+v", ok, terminal)
	}
}

func TestDurableCompatPlaybackStorePreservesPendingUpdateFromOlderSnapshot(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	local := PlaybackSession{ID: "play-1", CompatToken: "owner", UpstreamSessionID: "upstream-new"}
	store.mem.Put(local)
	store.appendPendingUpdate("play-1", "owner", func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-new"
		return nil
	})
	generation := store.tokenGenerationSnapshot("owner")
	durable := local
	durable.UpstreamSessionID = "upstream-old"

	if !store.applyCompatTokenSnapshot("owner", []PlaybackSession{durable}, generation) {
		t.Fatal("durable snapshot was unexpectedly discarded")
	}
	if got, ok := store.mem.Get("play-1"); !ok || got.UpstreamSessionID != "upstream-new" {
		t.Fatalf("pending local update was overwritten: ok=%v session=%+v", ok, got)
	}
}

func TestDurableCompatPlaybackStoreDropsPendingSessionMissingFromSnapshot(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	store.mem.Put(PlaybackSession{ID: "play-1", CompatToken: "owner", RouteItemID: "route-1"})
	store.appendPendingUpdate("play-1", "owner", func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-new"
		return nil
	})
	generation := store.tokenGenerationSnapshot("owner")

	if !store.applyCompatTokenSnapshot("owner", nil, generation) {
		t.Fatal("durable deletion snapshot was unexpectedly discarded")
	}
	if _, _, ok := store.mem.FindByRoute("owner", "route-1"); ok {
		t.Fatal("missing durable row remained routable due to a pending update")
	}
	if store.hasPendingUpdates("play-1") {
		t.Fatal("pending update survived authoritative durable deletion")
	}
}

func TestDurableCompatPlaybackStoreTokenSnapshotKeepsOtherTokenPendingUpdates(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	store.mem.Put(PlaybackSession{ID: "play-a", CompatToken: "owner-a"})
	store.mem.Put(PlaybackSession{ID: "play-b", CompatToken: "owner-b"})
	store.appendPendingUpdate("play-b", "owner-b", func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-b"
		return nil
	})
	generation := store.tokenGenerationSnapshot("owner-a")

	if !store.applyCompatTokenSnapshot(
		"owner-a", []PlaybackSession{{ID: "play-a", CompatToken: "owner-a"}}, generation,
	) {
		t.Fatal("token A snapshot was unexpectedly discarded")
	}
	if !store.hasPendingUpdates("play-b") {
		t.Fatal("token A snapshot discarded token B's pending update")
	}
}

func TestDurableCompatPlaybackStoreSerializesSameSessionMutations(t *testing.T) {
	store := NewDurableCompatPlaybackStore(nil, time.Hour, nil)
	unlock := store.lockSessionMutation("play-1")
	acquired := make(chan struct{})
	released := make(chan struct{})
	go func() {
		secondUnlock := store.lockSessionMutation("play-1")
		close(acquired)
		<-released
		secondUnlock()
	}()
	select {
	case <-acquired:
		t.Fatal("same-session mutation lock was acquired concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
		close(released)
	case <-time.After(time.Second):
		t.Fatal("same-session mutation did not resume after release")
	}
}

func TestDurableCompatPlaybackStorePersistsTerminalShell(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-terminal-shell-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })
	store := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	store.Put(PlaybackSession{ID: id, CompatToken: "owner", UpstreamSessionID: "upstream-1"})

	if err := store.HideFromRouting(id, "owner"); err != nil {
		t.Fatalf("persist terminal shell: %v", err)
	}
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if _, ok := fresh.Get(id); ok {
		t.Fatal("fresh process routed a durably hidden terminal shell")
	}
	terminal, ok := fresh.GetFinalizable(id, "owner")
	if !ok || !terminal.Terminal || terminal.TerminalScrobbleEvent != nil {
		t.Fatalf("terminal shell = ok=%v session=%+v", ok, terminal)
	}
	pending, err := fresh.ListPendingTerminals(context.Background(), 100)
	if err != nil {
		t.Fatalf("list terminal shells: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("terminal shell appeared as a pending event: %+v", pending)
	}
	claimUntil := time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute)
	if claimed, err := fresh.ClaimTerminal(id, "owner", claimUntil); err == nil || claimed != nil {
		t.Fatalf("terminal shell without an event was claimable: err=%v session=%+v", err, claimed)
	}
}

func TestDurableCompatPlaybackStoreTerminalizesConflictingUnpersistedShell(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-terminal-conflict-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })
	seed := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	seed.Put(PlaybackSession{ID: id, CompatToken: "owner", UpstreamSessionID: "upstream-1"})
	local := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	local.mem.Put(PlaybackSession{ID: id, CompatToken: "owner", UpstreamSessionID: "upstream-1"})
	local.markUnpersisted(id)

	if err := local.HideFromRouting(id, "owner"); err != nil {
		t.Fatalf("hide conflicting terminal shell: %v", err)
	}
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	if _, ok := fresh.Get(id); ok {
		t.Fatal("insert conflict left the durable session routable")
	}
}

func TestDurableCompatPlaybackStoreReplaysPendingUpdates(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-pending-update-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })
	store := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	store.Put(PlaybackSession{ID: id, CompatToken: "owner", UpstreamSessionID: "upstream-old"})
	if err := store.mem.Update(id, func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-new"
		return nil
	}); err != nil {
		t.Fatalf("seed pending cache update: %v", err)
	}
	store.appendPendingUpdate(id, "owner", func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-new"
		return nil
	})

	if err := store.Update(id, func(session *PlaybackSession) error {
		session.TranscodeStarted = true
		return nil
	}); err != nil {
		t.Fatalf("update with pending replay: %v", err)
	}
	if store.hasPendingUpdates(id) {
		t.Fatal("successfully replayed update remained pending")
	}
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	got, ok := fresh.Get(id)
	if !ok || got.UpstreamSessionID != "upstream-new" || !got.TranscodeStarted {
		t.Fatalf("durable replay lost updates: ok=%v session=%+v", ok, got)
	}
}

func TestDurableCompatPlaybackStoreColdLookupIgnoresReservedThrottle(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	id := fmt.Sprintf("compat-cold-load-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id) })
	seed := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	seed.Put(PlaybackSession{ID: id, CompatToken: "owner", RouteItemID: "route-1"})
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	_ = fresh.shouldRevalidateID(id)
	_ = fresh.shouldRevalidateToken("owner")

	if _, ok := fresh.Get(id); !ok {
		t.Fatal("cold ID lookup treated an in-flight validation reservation as not-found")
	}
	other := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	_ = other.shouldRevalidateToken("owner")
	if _, _, ok := other.FindByRoute("owner", "route-1"); !ok {
		t.Fatal("cold token lookup treated an in-flight validation reservation as not-found")
	}
}
