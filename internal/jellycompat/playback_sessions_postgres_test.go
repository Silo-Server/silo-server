package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchsync"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNegotiatedSessionAdvisoryLockKeyIsPinnedAndSeparatesRepresentativeInputs(t *testing.T) {
	t.Parallel()

	baseline := negotiatedSessionAdvisoryLockKey("token", "device", "route")
	const wantBaseline int64 = -2277851018424744338
	if baseline != wantBaseline {
		t.Fatalf("key = %d, want pinned value %d", baseline, wantBaseline)
	}

	testCases := []struct {
		name           string
		compatToken    string
		clientDeviceID string
		routeItemID    string
	}{
		{name: "baseline", compatToken: "token", clientDeviceID: "device", routeItemID: "route"},
		{name: "component boundary left", compatToken: "ab", clientDeviceID: "c", routeItemID: "d"},
		{name: "component boundary right", compatToken: "a", clientDeviceID: "bc", routeItemID: "d"},
		{name: "delimiter in token", compatToken: "a\x00b", clientDeviceID: "c", routeItemID: "d"},
		{name: "delimiter in device", compatToken: "a", clientDeviceID: "b\x00c", routeItemID: "d"},
		{name: "reordered components", compatToken: "device", clientDeviceID: "token", routeItemID: "route"},
		{name: "empty components", compatToken: "", clientDeviceID: "", routeItemID: ""},
		{
			name:           "long components",
			compatToken:    strings.Repeat("t", 4096),
			clientDeviceID: strings.Repeat("d", 4096),
			routeItemID:    strings.Repeat("r", 4096),
		},
	}

	seen := make(map[int64]string, len(testCases))
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := negotiatedSessionAdvisoryLockKey(tc.compatToken, tc.clientDeviceID, tc.routeItemID)
			if prior, exists := seen[key]; exists {
				t.Fatalf("unexpected representative collision with %q: %d", prior, key)
			}
			seen[key] = tc.name
		})
	}
}

func TestNegotiatedSessionAdvisoryLockQueriesArePinned(t *testing.T) {
	t.Parallel()

	if negotiatedSessionLegacyAdvisoryLockQuery != `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))` {
		t.Fatalf("legacy advisory lock query changed: %q", negotiatedSessionLegacyAdvisoryLockQuery)
	}
	if negotiatedSessionAdvisoryLockQuery != `SELECT pg_advisory_xact_lock($1::bigint)` {
		t.Fatalf("advisory lock query can accept text again: %q", negotiatedSessionAdvisoryLockQuery)
	}
}

type negotiatedSessionAdvisoryLockCall struct {
	query string
	args  []any
}

type negotiatedSessionAdvisoryLockRecorder struct {
	calls []negotiatedSessionAdvisoryLockCall
}

func (r *negotiatedSessionAdvisoryLockRecorder) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	r.calls = append(r.calls, negotiatedSessionAdvisoryLockCall{
		query: query,
		args:  append([]any(nil), args...),
	})
	return pgconn.CommandTag{}, nil
}

func TestAcquireNegotiatedSessionAdvisoryLockBridgesLegacyThenBigint(t *testing.T) {
	t.Parallel()

	recorder := &negotiatedSessionAdvisoryLockRecorder{}
	err := acquireNegotiatedSessionAdvisoryLock(
		context.Background(), recorder,
		"token\x00with-delimiter", "device", "route\x00item",
	)
	if err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}
	if len(recorder.calls) != 2 {
		t.Fatalf("call count = %d, want 2", len(recorder.calls))
	}
	legacy := recorder.calls[0]
	if legacy.query != negotiatedSessionLegacyAdvisoryLockQuery {
		t.Fatalf("legacy query = %q, want %q", legacy.query, negotiatedSessionLegacyAdvisoryLockQuery)
	}
	if len(legacy.args) != 1 {
		t.Fatalf("legacy argument count = %d, want 1", len(legacy.args))
	}
	legacyScope, ok := legacy.args[0].(string)
	if !ok {
		t.Fatalf("legacy advisory lock argument type = %T, want string", legacy.args[0])
	}
	if strings.ContainsRune(legacyScope, '\x00') {
		t.Fatalf("legacy advisory lock scope contains NUL: %q", legacyScope)
	}

	versioned := recorder.calls[1]
	if versioned.query != negotiatedSessionAdvisoryLockQuery {
		t.Fatalf("versioned query = %q, want %q", versioned.query, negotiatedSessionAdvisoryLockQuery)
	}
	if len(versioned.args) != 1 {
		t.Fatalf("versioned argument count = %d, want 1", len(versioned.args))
	}
	if _, ok := versioned.args[0].(int64); !ok {
		t.Fatalf("versioned advisory lock argument type = %T, want int64", versioned.args[0])
	}
}

func TestMarshalPlaybackSessionStripsNestedNUL(t *testing.T) {
	wantLiteral := `literal\u0000text`
	data, err := marshalPlaybackSession(PlaybackSession{
		ClientDeviceID: "device\x00id",
		MediaSources: []PlaybackMediaSource{{
			Version: catalog.FileVersion{
				FileName:   "episode\x00name.mkv",
				EditionRaw: wantLiteral,
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal playback session: %v", err)
	}
	var got PlaybackSession
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal sanitized playback session: %v", err)
	}
	if got.ClientDeviceID != "deviceid" || got.MediaSources[0].Version.FileName != "episodename.mkv" {
		t.Fatalf("nested NUL was retained: %#v", got)
	}
	if got.MediaSources[0].Version.EditionRaw != wantLiteral {
		t.Fatalf("literal escape changed: %q", got.MediaSources[0].Version.EditionRaw)
	}
}

func TestNegotiatedPlaybackScopeIsPostgresSafeAndUnambiguous(t *testing.T) {
	left := negotiatedPlaybackScope("ab", "c\x00", "d")
	right := negotiatedPlaybackScope("a", "bc", "d")
	if strings.ContainsRune(left, '\x00') {
		t.Fatalf("scope contains PostgreSQL-invalid NUL: %q", left)
	}
	if left == right {
		t.Fatalf("scope aliases distinct field tuples: %q", left)
	}
}

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

// TestPlaybackSessionStoreRetireChecksUpstreamUnderLock: an end looks a play
// up by its upstream session and then retires it. A method or upstream
// switch that lands between the two must leave the play it attached alone,
// so the retiring step checks the play again under the lock it marks it in.
func TestPlaybackSessionStoreRetireChecksUpstreamUnderLock(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "moved", CompatToken: "token", UpstreamSessionID: "new", UpstreamPlayMethod: "transcode"})
	store.Put(PlaybackSession{ID: "direct", CompatToken: "token", UpstreamSessionID: "old", UpstreamPlayMethod: "direct"})
	store.Put(PlaybackSession{ID: "current", CompatToken: "token", UpstreamSessionID: "old", UpstreamPlayMethod: "remux"})
	for id, want := range map[string]bool{"moved": false, "direct": false, "current": true} {
		if got := store.retireIfOnUpstream(id, "old"); got != want {
			t.Errorf("retire %s for the old upstream session = %v, want %v", id, got, want)
		}
		if _, routable := store.Get(id); routable == want {
			t.Errorf("%s routable = %v after the end", id, routable)
		}
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

// TestDurableCompatPlaybackStoreUpdateRejectionAcrossReplicas: two API
// replicas whose caches both hold the same upstream session race the
// compare-and-swap ensureUpstreamPlayback uses to attach a new one. The row
// lock serializes them, so the second must see the first's commit, get
// errUpstreamReplaced, and leave its cache matching the committed row rather
// than keeping a second upstream session of its own.
func TestDurableCompatPlaybackStoreUpdateRejectionAcrossReplicas(t *testing.T) {
	pool := newCompatTestPool(t)
	replicas := []*DurableCompatPlaybackStore{
		NewDurableCompatPlaybackStore(pool, time.Hour, nil),
		NewDurableCompatPlaybackStore(pool, time.Hour, nil),
	}
	id := t.Name()
	replicas[0].Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: "up-0"})
	t.Cleanup(func() { replicas[0].Delete(id) })
	for _, replica := range replicas {
		if got, ok := replica.Get(id); !ok || got.UpstreamSessionID != "up-0" {
			t.Fatalf("warm cache = %+v, %v", got, ok)
		}
	}

	errs := make([]error, len(replicas))
	start := make(chan struct{})
	done := make(chan int)
	for i, replica := range replicas {
		go func() {
			<-start
			errs[i] = replica.Update(id, func(current *PlaybackSession) error {
				if current.UpstreamSessionID != "up-0" {
					return errUpstreamReplaced
				}
				current.UpstreamSessionID = fmt.Sprintf("up-%c", 'A'+i)
				return nil
			})
			done <- i
		}()
	}
	close(start)
	<-done
	<-done

	row, ok, err := replicas[0].load(id)
	if err != nil || !ok {
		t.Fatalf("load row: %v, %v", ok, err)
	}
	winners := 0
	for i, replica := range replicas {
		switch {
		case errs[i] == nil:
			winners++
		case !errors.Is(errs[i], errUpstreamReplaced):
			t.Fatalf("replica %d Update err = %v, want nil or errUpstreamReplaced", i, errs[i])
		}
		cached, ok := replica.mem.Get(id)
		if !ok {
			t.Fatalf("replica %d dropped the session from its cache", i)
		}
		if cached.UpstreamSessionID != row.UpstreamSessionID {
			t.Errorf("replica %d cache upstream = %q, row upstream = %q", i, cached.UpstreamSessionID, row.UpstreamSessionID)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d (errs %v), want exactly one", winners, errs)
	}
}

// TestDurableCompatPlaybackStoreRetiresUpstreamSessionAcrossReplicas: an
// administrator's end can land on a replica that never served the play. The
// retirement must stop the replica that does serve it from routing the play:
// at once on the replica that retired it, and on the other once its cached
// copy is revalidated. A direct play is retired from neither.
func TestDurableCompatPlaybackStoreRetiresUpstreamSessionAcrossReplicas(t *testing.T) {
	pool := newCompatTestPool(t)
	now := time.Now()
	clock := func() time.Time { return now }
	holder := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	other := NewDurableCompatPlaybackStore(pool, time.Hour, clock)
	id := t.Name()
	for upstream, method := range map[string]string{"up-remote": "transcode", "up-repeat": "transcode", "up-local": "remux", "up-direct": "direct"} {
		holder.Put(PlaybackSession{ID: id + upstream, CompatToken: id, UpstreamSessionID: id + upstream, UpstreamPlayMethod: method})
		t.Cleanup(func() { holder.Delete(id + upstream) })
		if _, ok := holder.Get(id + upstream); !ok {
			t.Fatalf("holder does not route %s", upstream)
		}
	}

	for name, replica := range map[string]*DurableCompatPlaybackStore{"holder": holder, "other": other} {
		if retired, err := replica.RetireUpstreamSession(id + "up-direct"); err != nil || retired {
			t.Fatalf("retire a direct play from the %s = %v, %v, want it left alone", name, retired, err)
		}
	}

	if retired, err := other.RetireUpstreamSession(id + "up-remote"); err != nil || !retired {
		t.Fatalf("retire from the replica without the play = %v, %v", retired, err)
	}
	if retired, err := holder.RetireUpstreamSession(id + "up-local"); err != nil || !retired {
		t.Fatalf("retire from the replica holding the play = %v, %v", retired, err)
	}
	if _, ok := holder.Get(id + "up-local"); ok {
		t.Fatal("the replica that retired its own play still routes it")
	}
	// A repeat on the holder finds nothing left to retire, and stops the
	// holder routing its cached copy without waiting for a revalidation.
	if retired, err := other.RetireUpstreamSession(id + "up-repeat"); err != nil || !retired {
		t.Fatalf("retire before the repeat = %v, %v", retired, err)
	}
	if retired, err := holder.RetireUpstreamSession(id + "up-repeat"); err != nil || retired {
		t.Fatalf("repeat retire = %v, %v, want nothing left to retire", retired, err)
	}
	if _, ok := holder.Get(id + "up-repeat"); ok {
		t.Fatal("the holder still routes its cached copy after a repeat end")
	}
	now = now.Add(compatCacheRevalidationInterval)
	if _, ok := holder.Get(id + "up-remote"); ok {
		t.Fatal("the holder still routes a play another replica retired")
	}
	if _, ok := holder.Get(id + "up-direct"); !ok {
		t.Fatal("the holder no longer routes the direct play")
	}
}

// TestDurableCompatPlaybackStoreRetireLeavesMovedPlay: a replica whose cached
// copy still names an upstream session, and still owes the row an unrelated
// write, must not retire the play from that copy once another replica has
// moved it to a new upstream session. The row decides: it stays routable, the
// owed write still lands, and the replica adopts the row's state. The owed
// write is queued the way a failed Update queues it, which a live pool cannot
// be made to do.
func TestDurableCompatPlaybackStoreRetireLeavesMovedPlay(t *testing.T) {
	pool := newCompatTestPool(t)
	stale := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	mover := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	for _, method := range []string{"direct", "transcode"} {
		t.Run("moved to "+method, func(t *testing.T) {
			id := t.Name()
			oldUpstream, newUpstream := id+"-old", id+"-new"
			stale.Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: oldUpstream, UpstreamPlayMethod: "transcode"})
			t.Cleanup(func() { stale.Delete(id) })
			owed := func(session *PlaybackSession) error {
				session.ProgressPersistenceKnown = true
				return nil
			}
			if err := stale.mem.Update(id, owed); err != nil {
				t.Fatal(err)
			}
			stale.appendPendingUpdate(id, id, owed)
			if err := mover.Update(id, func(session *PlaybackSession) error {
				session.UpstreamSessionID, session.UpstreamPlayMethod = newUpstream, method
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			if retired, err := stale.RetireUpstreamSession(oldUpstream); err != nil || retired {
				t.Fatalf("retire the replaced upstream session = %v, %v, want nothing retired", retired, err)
			}
			row, ok, err := mover.load(id)
			if err != nil || !ok || row.Terminal || row.UpstreamSessionID != newUpstream || !row.ProgressPersistenceKnown {
				t.Fatalf("row = %+v, %v, %v; want the moved play routable with the owed write applied", row, ok, err)
			}
			if got, ok := stale.Get(id); !ok || got.UpstreamSessionID != newUpstream || got.UpstreamPlayMethod != method {
				t.Fatalf("stale replica reads %+v, %v; want the row's new upstream session", got, ok)
			}
		})
	}

	// The owed write is the attach itself: the row learns the upstream
	// session first, and is then retired by it.
	t.Run("owed attach", func(t *testing.T) {
		id := t.Name()
		stale.Put(PlaybackSession{ID: id, CompatToken: id})
		t.Cleanup(func() { stale.Delete(id) })
		attach := func(session *PlaybackSession) error {
			session.UpstreamSessionID, session.UpstreamPlayMethod = id+"-up", "transcode"
			return nil
		}
		if err := stale.mem.Update(id, attach); err != nil {
			t.Fatal(err)
		}
		stale.appendPendingUpdate(id, id, attach)
		if retired, err := stale.RetireUpstreamSession(id + "-up"); err != nil || !retired {
			t.Fatalf("retire = %v, %v", retired, err)
		}
		if row, ok, err := mover.load(id); err != nil || !ok || !row.Terminal || row.UpstreamSessionID != id+"-up" {
			t.Fatalf("row = %+v, %v, %v; want it retired under its upstream session", row, ok, err)
		}
	})

	// A play that never reached Postgres is hidden where it lives, and no
	// row is written for it.
	t.Run("never persisted", func(t *testing.T) {
		id := t.Name()
		stale.mem.Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: id + "-up", UpstreamPlayMethod: "remux"})
		stale.markUnpersisted(id)
		t.Cleanup(func() { stale.Delete(id) })
		if retired, err := stale.RetireUpstreamSession(id + "-up"); err != nil || !retired {
			t.Fatalf("retire = %v, %v", retired, err)
		}
		if _, ok := stale.Get(id); ok {
			t.Fatal("the hidden copy is still routable")
		}
		if _, ok, err := mover.load(id); err != nil || ok {
			t.Fatalf("a row was written for the hidden copy: %v, %v", ok, err)
		}
	})
}

// TestDurableCompatPlaybackStoreRetryAfterFailedStatement: a retirement whose
// statement fails while the play's attach is still owed hides the copy,
// keeps its owed writes, and reports the error. The retry replays them and
// retires the row in one transaction, so no other replica keeps routing the
// play. A row that another replica attached to its own upstream session in
// the meantime is left alone. The outage is a closed pool for that one call.
func TestDurableCompatPlaybackStoreRetryAfterFailedStatement(t *testing.T) {
	pool := newCompatTestPool(t)
	closed, err := pgxpool.New(t.Context(), os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	stale := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	mover := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprintf("row moved=%v", moved), func(t *testing.T) {
			id := t.Name()
			upstream, wantUpstream := id+"-up", id+"-up"
			stale.Put(PlaybackSession{ID: id, CompatToken: id})
			t.Cleanup(func() { stale.Delete(id) })
			attach := func(session *PlaybackSession) error {
				if session.UpstreamSessionID != "" {
					return errUpstreamReplaced
				}
				session.UpstreamSessionID, session.UpstreamPlayMethod = upstream, "transcode"
				return nil
			}
			if err := stale.mem.Update(id, attach); err != nil {
				t.Fatal(err)
			}
			stale.appendPendingUpdate(id, id, attach)
			stale.pool = closed
			hidden, outageErr := stale.RetireUpstreamSession(upstream)
			stale.pool = pool
			if outageErr == nil || !hidden {
				t.Fatalf("retire during the outage = %v, %v, want the copy hidden and an error to retry on", hidden, outageErr)
			}
			if _, ok := stale.Get(id); ok {
				t.Fatal("the copy is still routable after the failed statement")
			}
			if moved {
				wantUpstream = id + "-other"
				if err := mover.Update(id, func(session *PlaybackSession) error {
					session.UpstreamSessionID, session.UpstreamPlayMethod = wantUpstream, "direct"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}

			if retired, err := stale.RetireUpstreamSession(upstream); err != nil || retired == moved {
				t.Fatalf("retry = %v, %v, want retired = %v", retired, err, !moved)
			}
			if row, ok, err := mover.load(id); err != nil || !ok || row.Terminal == moved || row.UpstreamSessionID != wantUpstream {
				t.Fatalf("row = %+v, %v, %v; want terminal = %v on %s", row, ok, err, !moved, wantUpstream)
			}
			if _, ok := mover.Get(id); ok != moved {
				t.Fatalf("the other replica routes the play = %v, want %v", ok, moved)
			}
			if stale.hasPendingUpdates(id) {
				t.Fatal("the stale replica still owes writes")
			}
		})
	}
}

// TestDurableCompatPlaybackStoreRetireHidesCopyWhenReplayFails: the statement
// matches nothing because the row never received the attach, and replaying
// the owed attach then fails. That is a failed retirement, not an answer
// from the row: the copy is hidden with its owed writes and the error is
// reported, so the retry finishes it. The replay fails on a row another
// transaction holds locked; the store's connections give up on the lock
// after 200ms.
func TestDurableCompatPlaybackStoreRetireHidesCopyWhenReplayFails(t *testing.T) {
	pool := newCompatTestPool(t)
	config, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["lock_timeout"] = "200ms"
	impatient, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(impatient.Close)
	stale := NewDurableCompatPlaybackStore(impatient, time.Hour, nil)
	other := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	id := t.Name()
	upstream := id + "-up"
	stale.Put(PlaybackSession{ID: id, CompatToken: id})
	t.Cleanup(func() { stale.Delete(id) })
	attach := func(session *PlaybackSession) error {
		if session.UpstreamSessionID != "" {
			return errUpstreamReplaced
		}
		session.UpstreamSessionID, session.UpstreamPlayMethod = upstream, "transcode"
		return nil
	}
	if err := stale.mem.Update(id, attach); err != nil {
		t.Fatal(err)
	}
	stale.appendPendingUpdate(id, id, attach)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM jellycompat_playback_sessions WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	hidden, replayErr := stale.RetireUpstreamSession(upstream)
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if replayErr == nil || !hidden {
		t.Fatalf("retire with a failed replay = %v, %v, want the copy hidden and an error to retry on", hidden, replayErr)
	}
	if _, ok := stale.Get(id); ok {
		t.Fatal("the copy still routes the ended upstream session")
	}

	if retired, err := stale.RetireUpstreamSession(upstream); err != nil || !retired {
		t.Fatalf("retry = %v, %v, want the row retired", retired, err)
	}
	if row, ok, err := other.load(id); err != nil || !ok || !row.Terminal || row.UpstreamSessionID != upstream {
		t.Fatalf("row = %+v, %v, %v; want it retired under its upstream session", row, ok, err)
	}
	if _, ok := other.Get(id); ok {
		t.Fatal("another replica still routes the play")
	}
}

// TestDurableCompatPlaybackStoreDropsRejectedPendingUpdate: a write queued
// after a DB failure that no longer applies to the row (its compare-and-swap
// lost to another replica meanwhile) is dropped instead of blocking every
// later write to the session.
func TestDurableCompatPlaybackStoreDropsRejectedPendingUpdate(t *testing.T) {
	pool := newCompatTestPool(t)
	store := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	id := t.Name()
	store.Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: "up-0"})
	t.Cleanup(func() { store.Delete(id) })
	store.appendPendingUpdate(id, id, func(*PlaybackSession) error { return errUpstreamReplaced })

	if err := store.Update(id, func(current *PlaybackSession) error {
		current.ClientPlaySessionID = "client-psid"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	row, ok, err := store.load(id)
	if err != nil || !ok {
		t.Fatalf("load row: %v, %v", ok, err)
	}
	if row.ClientPlaySessionID != "client-psid" {
		t.Fatalf("row ClientPlaySessionID = %q, want the update persisted", row.ClientPlaySessionID)
	}
	if store.hasPendingUpdates(id) {
		t.Fatal("rejected pending update is still queued")
	}
}
