package sections

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func mediaItems(ids ...string) []*models.MediaItem {
	items := make([]*models.MediaItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, &models.MediaItem{ContentID: id})
	}
	return items
}

func itemIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

func staticLoader(items []*models.MediaItem, calls *int64) resolvedListLoader {
	return func(context.Context) ([]*models.MediaItem, int, error) {
		if calls != nil {
			atomic.AddInt64(calls, 1)
		}
		return items, len(items), nil
	}
}

// TestResolvedListCacheInvalidationReleasesSupersededEntries verifies a
// generation bump actually frees the entries it obsoletes — a long rescan bumps
// the generation every 30s, well inside the 15-minute TTL, so leaving them for
// the expiry sweep would keep many generations of every (library × scope) entry
// resident at once. Generation-independent entries must survive.
func TestResolvedListCacheInvalidationReleasesSupersededEntries(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	clock := time.Unix(1_700_000_000, 0)
	resolvedListInvalidationMu.Lock()
	resolvedListNow = func() time.Time { return clock }
	resolvedListInvalidationMu.Unlock()

	recent := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	genre := ResolvedSection{SectionType: SectionGenre, ItemLimit: 20}

	prime := func(key string) {
		t.Helper()
		if _, _, err := getOrRefresh(context.Background(), key, clock, staticLoader(mediaItems("cached"), nil)); err != nil {
			t.Fatalf("prime %q: %v", key, err)
		}
		if _, ok := resolvedListGet(key); !ok {
			t.Fatalf("entry %q should be cached right after priming", key)
		}
	}

	// Several access scopes of the generation-scoped rail, plus one rail that is
	// not generation scoped.
	var oldKeys []string
	for _, libraries := range [][]int{{1}, {2}, {3}} {
		key := resolvedListCacheKey(recent, nil, libraries, catalog.AccessFilter{})
		prime(key)
		oldKeys = append(oldKeys, key)
	}
	genreKey := resolvedListCacheKey(genre, nil, []int{1}, catalog.AccessFilter{})
	prime(genreKey)

	InvalidateResolvedListCache()

	for _, key := range oldKeys {
		if _, ok := resolvedListGet(key); ok {
			t.Fatalf("superseded entry %q was not released", key)
		}
	}
	if _, ok := resolvedListGet(genreKey); !ok {
		t.Fatal("generation-independent entry must survive an invalidation")
	}

	// The new generation caches normally and is itself released by the next bump.
	newKey := resolvedListCacheKey(recent, nil, []int{1}, catalog.AccessFilter{})
	resolvedListSet(newKey, mediaItems("refreshed"), 1, clock, resolvedListEvictionEpoch.Load())
	clock = clock.Add(resolvedListInvalidationInterval)
	InvalidateResolvedListCache()
	if _, ok := resolvedListGet(newKey); ok {
		t.Fatal("second bump did not release the previous generation's entry")
	}
	if _, ok := resolvedListGet(genreKey); !ok {
		t.Fatal("generation-independent entry must survive repeated invalidations")
	}
	// Idle scopes retain only their original lifetime across invalidations.
	clock = clock.Add(resolvedListTTL)
	InvalidateResolvedListCache()
	resolvedListCacheMu.RLock()
	size := len(resolvedListCache)
	resolvedListCacheMu.RUnlock()
	if size != 1 {
		t.Fatalf("cache holds %d entries after three bumps, want 1 (the generation-independent rail)", size)
	}
}

func TestResolvedListCacheInvalidationDebouncesScanBursts(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	now := time.Unix(1_700_000_000, 0)
	resolvedListInvalidationMu.Lock()
	resolvedListNow = func() time.Time { return now }
	resolvedListInvalidationMu.Unlock()

	var callers sync.WaitGroup
	for range 100 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			InvalidateResolvedListCache()
		}()
	}
	callers.Wait()
	if got := resolvedListGeneration.Load(); got != 1 {
		t.Fatalf("generation after burst = %d, want 1", got)
	}

	now = now.Add(29 * time.Second)
	InvalidateResolvedListCache()
	if got := resolvedListGeneration.Load(); got != 1 {
		t.Fatalf("generation inside debounce window = %d, want 1", got)
	}

	now = now.Add(time.Second)
	InvalidateResolvedListCache()
	if got := resolvedListGeneration.Load(); got != 2 {
		t.Fatalf("generation at debounce boundary = %d, want 2", got)
	}
}

func TestResolvedListCacheInvalidationRunsTrailingEdgeWithoutAnotherEvent(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	now := time.Unix(1_700_000_000, 0)
	var scheduled func()
	resolvedListInvalidationMu.Lock()
	resolvedListNow = func() time.Time { return now }
	resolvedListScheduleInvalidation = func(_ time.Duration, callback func()) func() {
		scheduled = callback
		return func() { scheduled = nil }
	}
	resolvedListInvalidationMu.Unlock()

	InvalidateResolvedListCache()
	now = now.Add(10 * time.Second)
	InvalidateResolvedListCache()
	if scheduled == nil {
		t.Fatal("suppressed invalidation did not schedule a trailing edge")
	}
	if got := resolvedListGeneration.Load(); got != 1 {
		t.Fatalf("generation before trailing edge = %d, want 1", got)
	}

	now = now.Add(20 * time.Second)
	scheduled()
	if got := resolvedListGeneration.Load(); got != 2 {
		t.Fatalf("generation after trailing edge = %d, want 2", got)
	}
}

func TestResolvedListCacheInvalidationIsolatesInflightRefresh(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	resolved := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	base := time.Unix(1_700_000_000, 0)
	oldKey := resolvedListCacheKey(resolved, nil, []int{7}, catalog.AccessFilter{})
	if _, _, err := getOrRefresh(context.Background(), oldKey, base, staticLoader(mediaItems("old"), nil)); err != nil {
		t.Fatalf("prime old generation: %v", err)
	}

	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	refreshDone := make(chan struct{})
	oldRefresh := func(context.Context) ([]*models.MediaItem, int, error) {
		close(refreshStarted)
		<-releaseRefresh
		close(refreshDone)
		return mediaItems("late-stale"), 1, nil
	}
	if items, _, err := getOrRefresh(context.Background(), oldKey, base.Add(6*time.Minute), oldRefresh); err != nil || itemIDs(items)[0] != "old" {
		t.Fatalf("start old refresh: items %v, err %v", itemIDs(items), err)
	}
	<-refreshStarted

	InvalidateResolvedListCache()
	newKey := resolvedListCacheKey(resolved, nil, []int{7}, catalog.AccessFilter{})
	if _, _, err := getOrRefresh(context.Background(), newKey, base.Add(6*time.Minute), staticLoader(mediaItems("fresh"), nil)); err != nil {
		t.Fatalf("load active generation: %v", err)
	}
	close(releaseRefresh)
	<-refreshDone

	if !waitFor(2*time.Second, func() bool {
		entry, ok := resolvedListGet(oldKey)
		return ok && len(entry.items) == 1 && entry.items[0].ContentID == "late-stale"
	}) {
		t.Fatal("old generation refresh did not finish")
	}
	active, _, err := getOrRefresh(context.Background(), newKey, base.Add(6*time.Minute), func(context.Context) ([]*models.MediaItem, int, error) {
		t.Fatal("late old-generation refresh repopulated the active key")
		return nil, 0, nil
	})
	if err != nil || len(active) != 1 || active[0].ContentID != "fresh" {
		t.Fatalf("active generation = %v, err %v", itemIDs(active), err)
	}
}

// TestResolvedListCacheKeyScopeStillIsolatesWithoutID is the SECURITY guarantee:
// with the section ID removed from the key, two requests that are identical
// except for access scope (library, max content rating, or allowed-content-ids)
// must STILL hash to distinct keys so one profile can never be served another's
// membership.
func TestResolvedListCacheKeyScopeStillIsolatesWithoutID(t *testing.T) {
	// Deliberately identical section identity to prove scope alone splits the key.
	resolved := ResolvedSection{ID: "same-id", SectionType: SectionRecentlyAdded, ItemLimit: 24, Config: json.RawMessage(`{"filter_type":"movie"}`)}
	base := catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	lib1, lib2 := 7, 8

	baseline := resolvedListCacheKey(resolved, &lib1, nil, base)

	// Different presentation library.
	if resolvedListCacheKey(resolved, &lib2, nil, base) == baseline {
		t.Fatalf("different library must isolate the key even with identical section ID")
	}
	// Different max content rating.
	rating := base
	rating.MaxContentRating = "R"
	if resolvedListCacheKey(resolved, &lib1, nil, rating) == baseline {
		t.Fatalf("different max content rating must isolate the key")
	}
	// Different allowed-content-ids (content-restricted profile).
	restricted := base
	restricted.AllowedContentIDs = []string{"c1", "c2"}
	if resolvedListCacheKey(resolved, &lib1, nil, restricted) == baseline {
		t.Fatalf("different allowed-content-ids must isolate the key")
	}
	// Different accessible library set.
	accessible := base
	accessible.AllowedLibraryIDs = []int{1, 2, 3}
	if resolvedListCacheKey(resolved, &lib1, nil, accessible) == baseline {
		t.Fatalf("different accessible library set must isolate the key")
	}
	// Different disabled library set.
	disabled := base
	disabled.DisabledLibraryIDs = []int{9}
	if resolvedListCacheKey(resolved, &lib1, nil, disabled) == baseline {
		t.Fatalf("different disabled library set must isolate the key")
	}
	// Different excluded media types.
	excluded := base
	excluded.ExcludedMediaTypes = []string{"audiobook"}
	if resolvedListCacheKey(resolved, &lib1, nil, excluded) == baseline {
		t.Fatalf("different excluded media types must isolate the key")
	}
}

// TestResolvedListCacheKeyContentBoundaries verifies the content-scoping access
// boundaries (AllowedContentIDs, NamePrefix) that the filter-driven builders
// enforce as SQL constraints each produce a distinct key, so a content-restricted
// profile can never be served an unrestricted profile's membership.
func TestResolvedListCacheKeyContentBoundaries(t *testing.T) {
	resolved := ResolvedSection{ID: "sec-1", SectionType: SectionGenre, ItemLimit: 20}
	base := catalog.AccessFilter{AllowedLibraryIDs: []int{1}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}

	// nil (unrestricted) vs empty (restrict-to-nothing) vs a concrete allow-list
	// must all differ, and two different allow-lists must differ.
	unrestricted := resolvedListCacheKey(resolved, nil, nil, base)

	restrictNone := base
	restrictNone.AllowedContentIDs = []string{}
	keyRestrictNone := resolvedListCacheKey(resolved, nil, nil, restrictNone)

	allowX := base
	allowX.AllowedContentIDs = []string{"c1", "c2"}
	keyAllowX := resolvedListCacheKey(resolved, nil, nil, allowX)

	allowY := base
	allowY.AllowedContentIDs = []string{"c3"}
	keyAllowY := resolvedListCacheKey(resolved, nil, nil, allowY)

	for _, pair := range [][2]string{
		{unrestricted, keyRestrictNone},
		{unrestricted, keyAllowX},
		{keyRestrictNone, keyAllowX},
		{keyAllowX, keyAllowY},
	} {
		if pair[0] == pair[1] {
			t.Fatalf("expected distinct keys for distinct content scopes, got identical %q", pair[0])
		}
	}

	// Order-independence: the same allow-list in a different order shares a key.
	allowXReordered := base
	allowXReordered.AllowedContentIDs = []string{"c2", "c1"}
	if resolvedListCacheKey(resolved, nil, nil, allowXReordered) != keyAllowX {
		t.Fatalf("allow-list key should be order-independent")
	}

	// NamePrefix is an access boundary too.
	prefixed := base
	prefixed.NamePrefix = "The "
	if resolvedListCacheKey(resolved, nil, nil, prefixed) == unrestricted {
		t.Fatalf("NamePrefix must change the cache key")
	}
}

// TestResolvedListCacheDefensiveCopy verifies a caller mutating the returned
// slice cannot corrupt the cached entry.
func TestResolvedListCacheDefensiveCopy(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	key := "defensive-copy"
	now := time.Unix(1_700_000_000, 0)

	got, _, err := getOrRefresh(context.Background(), key, now, staticLoader(mediaItems("x1", "x2"), nil))
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	got[0] = &models.MediaItem{ContentID: "tampered"}

	reGot, _, err := getOrRefresh(context.Background(), key, now, func(context.Context) ([]*models.MediaItem, int, error) {
		t.Fatalf("loader should not run while entry is fresh")
		return nil, 0, nil
	})
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if reGot[0].ContentID != "x1" {
		t.Fatalf("cached entry was corrupted by caller mutation: got %q", reGot[0].ContentID)
	}
}

// TestResolvedListCacheCollectionUserExclusion verifies library collections are
// cacheable while user (profile-scoped) collections are not.
func TestResolvedListCacheCollectionUserExclusion(t *testing.T) {
	f := &Fetcher{}
	libraryCollection := ResolvedSection{
		SectionType: SectionCollection,
		Config:      mustJSON(t, SectionCollectionConfig{LibraryCollectionID: "lib-coll-1"}),
	}
	if !f.isCacheableSectionType(libraryCollection) {
		t.Fatalf("library collection should be cacheable")
	}

	userCollection := ResolvedSection{
		SectionType: SectionCollection,
		Config:      mustJSON(t, SectionCollectionConfig{UserCollectionID: "user-coll-1"}),
	}
	if f.isCacheableSectionType(userCollection) {
		t.Fatalf("user collection must NOT be cacheable (profile-scoped)")
	}
}

// TestResolvedListCacheStampede verifies N concurrent cold requests for the
// same key invoke the loader exactly once.
func TestResolvedListCacheStampede(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	key := "stampede"
	now := time.Unix(1_700_000_000, 0)

	const n = 50
	var calls int64
	release := make(chan struct{})
	loader := func(context.Context) ([]*models.MediaItem, int, error) {
		atomic.AddInt64(&calls, 1)
		// Block until every goroutine has entered singleflight so the collapse
		// window is guaranteed to overlap.
		<-release
		return mediaItems("shared"), 1, nil
	}

	var started sync.WaitGroup
	var finished sync.WaitGroup
	started.Add(n)
	finished.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer finished.Done()
			started.Done()
			items, _, err := getOrRefresh(context.Background(), key, now, loader)
			if err != nil {
				t.Errorf("stampede read: %v", err)
				return
			}
			if ids := itemIDs(items); len(ids) != 1 || ids[0] != "shared" {
				t.Errorf("stampede read returned %v, want [shared]", ids)
			}
		}()
	}

	// Give every goroutine time to reach the singleflight flight before letting
	// the single in-flight loader complete.
	started.Wait()
	time.Sleep(50 * time.Millisecond)
	close(release)
	finished.Wait()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("loader invoked %d times, want exactly 1", got)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}
	return b
}

// waitFor polls cond until it returns true or timeout elapses. Used instead of
// a fixed sleep to observe the async rebuild swap deterministically.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// TestBlockingRebuildDetachedFromLeaderCancellation verifies a cold-miss
// blocking rebuild survives the initiating request's cancellation: singleflight
// shares one build across every collapsed waiter, so the leader's client
// disconnecting must not fail the build. The leader itself stops waiting at
// once with its context's error, and a later caller reads the shared result.
func TestBlockingRebuildDetachedFromLeaderCancellation(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	loadErr := make(chan error, 1)
	loader := func(loadCtx context.Context) ([]*models.MediaItem, int, error) {
		calls.Add(1)
		close(entered)
		<-release
		// The loader honors ctx like the real fetchers do: without detachment
		// the leader's cancellation would fail the shared build here.
		loadErr <- loadCtx.Err()
		if err := loadCtx.Err(); err != nil {
			return nil, 0, err
		}
		return mediaItems("x1"), 1, nil
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	now := time.Unix(1_700_000_000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := getOrRefresh(ctx, "detach-key", now, loader)
		leaderDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the rebuild never started")
	}

	// The leader gives up while the build is still running.
	cancel()
	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled waiter kept waiting on the shared rebuild")
	}

	// A caller whose context is already done starts no second build.
	if _, _, err := getOrRefresh(ctx, "detach-key", now, loader); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled caller returned %v, want context.Canceled", err)
	}

	close(release)
	select {
	case err := <-loadErr:
		if err != nil {
			t.Fatalf("the detached build saw the leader's cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the rebuild never finished")
	}
	if !waitFor(5*time.Second, func() bool {
		_, ok := resolvedListGet("detach-key")
		return ok
	}) {
		t.Fatal("detached rebuild did not cache its result")
	}

	// A later caller reads the shared result without another build.
	items, total, err := getOrRefresh(context.Background(), "detach-key", now, loader)
	if err != nil {
		t.Fatalf("later caller: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ContentID != "x1" {
		t.Fatalf("unexpected result: items=%v total=%d", itemIDs(items), total)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("loader invoked %d times, want exactly 1", got)
	}
}

// TestBlockingRebuildPanicBecomesError verifies a loader panic on the
// detached build goroutine reaches the waiter as an error instead of
// crashing the process.
func TestBlockingRebuildPanicBecomesError(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	_, _, err := getOrRefresh(context.Background(), "panic-key", time.Unix(1_700_000_000, 0), func(context.Context) ([]*models.MediaItem, int, error) {
		panic("loader exploded")
	})
	if err == nil {
		t.Fatal("a panicking rebuild returned no error")
	}
	if _, ok := resolvedListGet("panic-key"); ok {
		t.Fatal("a panicking rebuild cached a result")
	}
}

// A scan must not make every waiting client pay for the same expensive rebuild.
func TestResolvedListCacheScanRefreshServesBoundedMembership(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()
	now := time.Now()
	resolvedListNow = func() time.Time { return now }
	sec := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	oldKey := resolvedListCacheKey(sec, nil, []int{7}, catalog.AccessFilter{})
	resolvedListSet(oldKey, mediaItems("old"), 1, now, resolvedListEvictionEpoch.Load())
	InvalidateResolvedListCache()
	// No readers during this interval: a scan must not make an idle scope cold.
	now = now.Add(2 * time.Minute)
	key := resolvedListCacheKey(sec, nil, []int{7}, catalog.AccessFilter{})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loader := func(context.Context) ([]*models.MediaItem, int, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return mediaItems("new"), 1, nil
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	for range 100 {
		items, _, err := getOrRefresh(t.Context(), key, now, loader)
		if err != nil || !slices.Equal(itemIDs(items), []string{"old"}) {
			t.Fatalf("during refresh: %v, %v", itemIDs(items), err)
		}
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not start")
	}
	if calls.Load() != 1 {
		t.Fatalf("loaders = %d", calls.Load())
	}
	entry, _ := resolvedListGet(key)
	if !entry.expiresAt.Equal(now.Add(resolvedListInvalidationGrace)) {
		t.Fatalf("expiry = %s", entry.expiresAt)
	}
	otherKey := resolvedListCacheKey(sec, nil, []int{8}, catalog.AccessFilter{})
	if _, ok := resolvedListGet(otherKey); ok {
		t.Fatal("fallback crossed a library scope")
	}
	close(release)
	if !waitFor(2*time.Second, func() bool {
		e, ok := resolvedListGet(key)
		return ok && len(e.items) > 0 && e.items[0].ContentID == "new"
	}) {
		t.Fatal("fresh membership did not replace fallback")
	}
}

func TestResolvedListCacheScanGraceExpiresAfterFailedRefresh(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()
	now := time.Now()
	resolvedListNow = func() time.Time { return now }
	sec := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	key := resolvedListCacheKey(sec, nil, []int{7}, catalog.AccessFilter{})
	resolvedListSet(key, mediaItems("old"), 1, now, resolvedListEvictionEpoch.Load())
	InvalidateResolvedListCache()
	key = resolvedListCacheKey(sec, nil, []int{7}, catalog.AccessFilter{})
	_, _, err := getOrRefresh(t.Context(), key, now, func(context.Context) ([]*models.MediaItem, int, error) {
		return nil, 0, errors.New("test refresh failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Second, func() bool {
		resolvedListRefreshMu.Lock()
		defer resolvedListRefreshMu.Unlock()
		_, running := resolvedListRefreshing[key]
		return !running
	}) {
		t.Fatal("failed refresh did not finish")
	}
	// Even when refreshes have produced no replacement, the grace deadline is
	// a hard boundary. A blocking load must supply the result after it.
	items, _, err := getOrRefresh(t.Context(), key, now.Add(resolvedListInvalidationGrace), staticLoader(mediaItems("fresh"), nil))
	if err != nil || !slices.Equal(itemIDs(items), []string{"fresh"}) {
		t.Fatalf("after deadline: %v, %v", itemIDs(items), err)
	}
}

func TestResolvedListCacheScanPreservesConcurrentNewGenerationLoad(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()
	now := time.Now()
	sec := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	oldKey := resolvedListCacheKey(sec, nil, []int{7}, catalog.AccessFilter{})
	resolvedListSet(oldKey, mediaItems("old"), 1, now, resolvedListEvictionEpoch.Load())
	// Reproduce a reader finishing after namespace publication but before the
	// invalidator has acquired the cache lock to carry fallback entries.
	generation := resolvedListGeneration.Add(1)
	current := resolvedListCacheKey(sec, nil, []int{7}, catalog.AccessFilter{})
	resolvedListSet(current, mediaItems("fresh"), 1, now, resolvedListEvictionEpoch.Load())
	resolvedListInvalidationMu.Lock()
	dropSupersededResolvedListEntries(generation)
	resolvedListInvalidationMu.Unlock()
	entry, ok := resolvedListGet(current)
	if !ok || !slices.Equal(itemIDs(entry.items), []string{"fresh"}) {
		t.Fatal("invalidation replaced the completed current-generation load")
	}
	if !entry.expiresAt.Equal(now.Add(resolvedListTTL)) {
		t.Fatal("invalidation shortened a fresh entry's lifetime")
	}
}

// TestEvictResolvedListItemsRebuildsOnlyListsWithTheItem covers an admin edit:
// every cached list holding the edited item, with or without a scan generation,
// rebuilds on its next read, and lists without it keep serving from the cache.
func TestEvictResolvedListItemsRebuildsOnlyListsWithTheItem(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	now := time.Unix(1_700_000_000, 0)
	added := resolvedListCacheKey(ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}, nil, []int{1}, catalog.AccessFilter{})
	released := resolvedListCacheKey(ResolvedSection{SectionType: SectionRecentlyReleased, ItemLimit: 20}, nil, []int{1}, catalog.AccessFilter{})
	other := resolvedListCacheKey(ResolvedSection{SectionType: SectionRecentlyReleased, ItemLimit: 20}, nil, []int{2}, catalog.AccessFilter{})
	episodes := resolvedListCacheKey(ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}, nil, []int{2}, catalog.AccessFilter{})
	for key, ids := range map[string][]string{
		added:    {"edited", "a"},
		released: {"b", "edited"},
		// An episode card shows its series' artwork.
		episodes: {"episode-tvdb-296762-1-5"},
		other:    {"c", "episode-tvdb-81189-1-1"},
	} {
		if _, _, err := getOrRefresh(context.Background(), key, now, staticLoader(mediaItems(ids...), nil)); err != nil {
			t.Fatalf("prime %q: %v", key, err)
		}
	}

	EvictResolvedListItems("edited", "series-tvdb-296762")

	var rebuilds int64
	for _, key := range []string{added, released, episodes} {
		items, _, err := getOrRefresh(context.Background(), key, now.Add(time.Minute), staticLoader(mediaItems("rebuilt"), &rebuilds))
		if err != nil || len(items) != 1 || items[0].ContentID != "rebuilt" {
			t.Fatalf("list with the edited item served %v, err %v; want a rebuild", itemIDs(items), err)
		}
	}
	if rebuilds != 3 {
		t.Fatalf("rebuilds = %d, want 3", rebuilds)
	}
	items, _, err := getOrRefresh(context.Background(), other, now.Add(time.Minute), func(context.Context) ([]*models.MediaItem, int, error) {
		t.Fatal("list without the edited item was rebuilt")
		return nil, 0, nil
	})
	if err != nil || len(items) != 2 || items[0].ContentID != "c" {
		t.Fatalf("unrelated list = %v, err %v", itemIDs(items), err)
	}
}

// TestEvictResolvedListItemsDropsLoadStartedBeforeTheEdit covers a background
// refresh that read the item before the edit and finishes after the eviction:
// its result must not be installed, or the old title would come back.
func TestEvictResolvedListItemsDropsLoadStartedBeforeTheEdit(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	base := time.Unix(1_700_000_000, 0)
	key := resolvedListCacheKey(ResolvedSection{SectionType: SectionRecentlyReleased, ItemLimit: 20}, nil, []int{1}, catalog.AccessFilter{})
	if _, _, err := getOrRefresh(context.Background(), key, base, staticLoader(mediaItems("edited"), nil)); err != nil {
		t.Fatalf("prime: %v", err)
	}

	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	staleRefresh := func(context.Context) ([]*models.MediaItem, int, error) {
		close(refreshStarted)
		<-releaseRefresh
		return mediaItems("edited", "read-before-edit"), 2, nil
	}
	// Past refreshAfter: serves the cached list and starts a background load.
	if _, _, err := getOrRefresh(context.Background(), key, base.Add(6*time.Minute), staleRefresh); err != nil {
		t.Fatalf("start refresh: %v", err)
	}
	<-refreshStarted
	EvictResolvedListItems("edited")
	close(releaseRefresh)

	if !waitFor(2*time.Second, func() bool {
		resolvedListRefreshMu.Lock()
		defer resolvedListRefreshMu.Unlock()
		_, inflight := resolvedListRefreshing[key]
		return !inflight
	}) {
		t.Fatal("background refresh did not finish")
	}
	if entry, ok := resolvedListGet(key); ok {
		t.Fatalf("refresh started before the eviction installed %v", itemIDs(entry.items))
	}
}

// A request that arrives after an admin edit can join a cold rebuild that
// started before it. It must not be handed that rebuild's pre-edit list.
func TestEvictResolvedListItemsRetriesCallerThatJoinedAnOlderRebuild(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	now := time.Unix(1_700_000_000, 0)
	key := resolvedListCacheKey(ResolvedSection{SectionType: SectionRecentlyReleased, ItemLimit: 20}, nil, []int{1}, catalog.AccessFilter{})

	started := make(chan struct{})
	release := make(chan struct{})
	staleDone := make(chan []string, 1)
	go func() {
		items, _, _ := getOrRefresh(context.Background(), key, now, func(context.Context) ([]*models.MediaItem, int, error) {
			close(started)
			<-release
			return mediaItems("read-before-edit"), 1, nil
		})
		staleDone <- itemIDs(items)
	}()
	<-started
	EvictResolvedListItems("edited")

	freshDone := make(chan []string, 1)
	go func() {
		items, _, _ := getOrRefresh(context.Background(), key, now, staticLoader(mediaItems("fresh"), nil))
		freshDone <- itemIDs(items)
	}()
	// Give the second caller time to join the in-flight rebuild. If it hasn't
	// joined yet it starts its own rebuild, which also yields "fresh", so the
	// pause can't make this test fail spuriously.
	time.Sleep(20 * time.Millisecond) //nolint:forbidigo // singleflight exposes no signal for a caller joining a flight.
	close(release)

	if got := <-freshDone; len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("caller after the eviction got %v, want a rebuild after the edit", got)
	}
	if got := <-staleDone; len(got) != 1 || got[0] != "read-before-edit" {
		t.Fatalf("caller before the eviction got %v", got)
	}
}

// Recently added TV rows hold only arrivals within the viewer's playback
// quality ceiling, so a row cached for a viewer with one must not be served
// to a viewer with another, or to one with none.
func TestRecentlyAddedCacheKeySeparatesQualityCeilings(t *testing.T) {
	recent := ResolvedSection{SectionType: SectionRecentlyAdded, ItemLimit: 20}
	keys := map[string]string{}
	for _, ceiling := range []string{"", "1080p", "2160p"} {
		key := resolvedListCacheKey(recent, nil, []int{1}, catalog.AccessFilter{MaxPlaybackQuality: ceiling})
		if other, ok := keys[key]; ok {
			t.Fatalf("ceilings %q and %q share the key %q", other, ceiling, key)
		}
		keys[key] = ceiling
	}
}
