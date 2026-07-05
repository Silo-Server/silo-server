package sections

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestResolvedListCacheScopeIsolation verifies distinct access scopes hash to
// distinct keys and never cross-serve one another's items.
func TestResolvedListCacheScopeIsolation(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	resolved := ResolvedSection{
		ID:          "sec-1",
		SectionType: SectionRecentlyAdded,
		ItemLimit:   20,
	}
	scopeA := catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaxContentRating: "PG-13"}
	scopeB := catalog.AccessFilter{AllowedLibraryIDs: []int{3}, MaxContentRating: "R"}

	keyA := resolvedListCacheKey(resolved, nil, nil, scopeA)
	keyB := resolvedListCacheKey(resolved, nil, nil, scopeB)
	if keyA == keyB {
		t.Fatalf("expected distinct keys for distinct scopes, got %q for both", keyA)
	}

	now := time.Unix(1_700_000_000, 0)
	itemsA := mediaItems("a1", "a2")
	itemsB := mediaItems("b1")

	gotA, totalA, err := getOrRefresh(context.Background(), keyA, now, staticLoader(itemsA, nil))
	if err != nil {
		t.Fatalf("scope A load: %v", err)
	}
	gotB, totalB, err := getOrRefresh(context.Background(), keyB, now, staticLoader(itemsB, nil))
	if err != nil {
		t.Fatalf("scope B load: %v", err)
	}

	if got := itemIDs(gotA); len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Fatalf("scope A returned %v, want [a1 a2]", got)
	}
	if totalA != 2 {
		t.Fatalf("scope A total = %d, want 2", totalA)
	}
	if got := itemIDs(gotB); len(got) != 1 || got[0] != "b1" {
		t.Fatalf("scope B returned %v, want [b1]", got)
	}
	if totalB != 1 {
		t.Fatalf("scope B total = %d, want 1", totalB)
	}

	// Re-reading scope A must still yield scope A's items even after scope B was
	// populated: no cross-serving between scopes.
	reGotA, _, err := getOrRefresh(context.Background(), keyA, now, func(context.Context) ([]*models.MediaItem, int, error) {
		t.Fatalf("scope A loader should not run again while entry is fresh")
		return nil, 0, nil
	})
	if err != nil {
		t.Fatalf("scope A re-read: %v", err)
	}
	if got := itemIDs(reGotA); len(got) != 2 || got[0] != "a1" {
		t.Fatalf("scope A re-read returned %v, want [a1 a2]", got)
	}
}

// TestResolvedListCacheKeyIgnoresSectionID is the core Approach-2 guarantee: two
// sections that share type+config+limit+scope but carry different arbitrary IDs
// hash to the SAME key. This is what lets a native "recently added" rail and the
// jellyfin-compat /Items/Latest collapse to one shared cache entry.
func TestResolvedListCacheKeyIgnoresSectionID(t *testing.T) {
	cfg := json.RawMessage(`{"filter_type":"movie"}`)
	scope := catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaxContentRating: "PG-13"}
	libraryID := 7

	native := ResolvedSection{ID: "home-rail-42", SectionType: SectionRecentlyAdded, ItemLimit: 24, Config: cfg}
	compat := ResolvedSection{ID: "compat-latest", SectionType: SectionRecentlyAdded, ItemLimit: 24, Config: cfg}

	keyNative := resolvedListCacheKey(native, &libraryID, nil, scope)
	keyCompat := resolvedListCacheKey(compat, &libraryID, nil, scope)
	if keyNative != keyCompat {
		t.Fatalf("expected identical keys regardless of section ID, got %q vs %q", keyNative, keyCompat)
	}

	// A differing type, config, limit, library, or scope must still split the key
	// — the ID is the only field that no longer participates.
	if resolvedListCacheKey(ResolvedSection{ID: "x", SectionType: SectionRecentlyReleased, ItemLimit: 24, Config: cfg}, &libraryID, nil, scope) == keyNative {
		t.Fatalf("different section type must change the key")
	}
	if resolvedListCacheKey(ResolvedSection{ID: "x", SectionType: SectionRecentlyAdded, ItemLimit: 12, Config: cfg}, &libraryID, nil, scope) == keyNative {
		t.Fatalf("different item limit must change the key")
	}
	if resolvedListCacheKey(native, &libraryID, nil, catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaxContentRating: "R"}) == keyNative {
		t.Fatalf("different max content rating must change the key")
	}
	otherLibrary := 8
	if resolvedListCacheKey(native, &otherLibrary, nil, scope) == keyNative {
		t.Fatalf("different library must change the key")
	}
}

// TestResolvedListCacheSharedAcrossSurfaces proves the WIN end-to-end through
// getOrRefresh: a native recently-added section and the compat /Items/Latest for
// the same library+type+limit+scope build the shared list exactly ONCE and reuse
// it across both surfaces.
func TestResolvedListCacheSharedAcrossSurfaces(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	cfg := json.RawMessage(`{"filter_type":"movie"}`)
	scope := catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaxContentRating: "PG-13"}
	libraryID := 7
	now := time.Unix(1_700_000_000, 0)

	native := ResolvedSection{ID: "home-recent-1", SectionType: SectionRecentlyAdded, ItemLimit: 24, Config: cfg}
	compat := ResolvedSection{ID: "compat-latest", SectionType: SectionRecentlyAdded, ItemLimit: 24, Config: cfg}

	keyNative := resolvedListCacheKey(native, &libraryID, nil, scope)
	keyCompat := resolvedListCacheKey(compat, &libraryID, nil, scope)

	var calls int64
	shared := mediaItems("m1", "m2", "m3")

	// Native surface builds the list first (cold miss → loader runs).
	gotNative, _, err := getOrRefresh(context.Background(), keyNative, now, staticLoader(shared, &calls))
	if err != nil {
		t.Fatalf("native load: %v", err)
	}
	// Compat surface hits the SAME entry: the loader must NOT run again.
	gotCompat, _, err := getOrRefresh(context.Background(), keyCompat, now, func(context.Context) ([]*models.MediaItem, int, error) {
		t.Fatalf("compat loader must not run: the native entry should be shared")
		return nil, 0, nil
	})
	if err != nil {
		t.Fatalf("compat load: %v", err)
	}

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("shared list built %d times, want exactly 1", got)
	}
	if a, b := itemIDs(gotNative), itemIDs(gotCompat); len(a) != 3 || len(b) != 3 || a[0] != b[0] || a[2] != b[2] {
		t.Fatalf("surfaces returned divergent lists: native=%v compat=%v", a, b)
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
	base := catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaxContentRating: "PG-13"}
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

// TestResolvedListCacheEvictsExpiredEntries verifies expired entries are swept
// so the process-global map cannot grow unbounded across never-repeated scopes.
func TestResolvedListCacheEvictsExpiredEntries(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	base := time.Unix(1_700_000_000, 0)
	if _, _, err := getOrRefresh(context.Background(), "scope-A", base, staticLoader(mediaItems("a1"), nil)); err != nil {
		t.Fatalf("prime scope-A: %v", err)
	}
	if _, ok := resolvedListGet("scope-A"); !ok {
		t.Fatalf("scope-A should be cached right after priming")
	}

	// A later write, past scope-A's expiry (base+15m) and the prune interval,
	// must sweep scope-A while installing scope-B.
	later := base.Add(20 * time.Minute)
	if _, _, err := getOrRefresh(context.Background(), "scope-B", later, staticLoader(mediaItems("b1"), nil)); err != nil {
		t.Fatalf("prime scope-B: %v", err)
	}
	if _, ok := resolvedListGet("scope-A"); ok {
		t.Fatalf("expired scope-A should have been evicted")
	}
	if _, ok := resolvedListGet("scope-B"); !ok {
		t.Fatalf("scope-B should be present")
	}
}

// TestResolvedListCacheKeyContentBoundaries verifies the content-scoping access
// boundaries (AllowedContentIDs, NamePrefix) that the filter-driven builders
// enforce as SQL constraints each produce a distinct key, so a content-restricted
// profile can never be served an unrestricted profile's membership.
func TestResolvedListCacheKeyContentBoundaries(t *testing.T) {
	resolved := ResolvedSection{ID: "sec-1", SectionType: SectionGenre, ItemLimit: 20}
	base := catalog.AccessFilter{AllowedLibraryIDs: []int{1}, MaxContentRating: "PG-13"}

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

// TestHashSectionConfigCanonicalizes verifies configs that are semantically
// identical but differ in whitespace or field order hash the same, so native
// and jellycompat fetches of the same rail share a cache entry. It fails if the
// canonicalization step is removed.
func TestHashSectionConfigCanonicalizes(t *testing.T) {
	a := hashSectionConfig(json.RawMessage(`{"sort":"added","order":"desc"}`))
	reordered := hashSectionConfig(json.RawMessage(`{"order":"desc","sort":"added"}`))
	spaced := hashSectionConfig(json.RawMessage("{ \"sort\":  \"added\" ,\n\"order\": \"desc\" }"))
	if a != reordered {
		t.Fatalf("field-order should not change the config hash: %q vs %q", a, reordered)
	}
	if a != spaced {
		t.Fatalf("whitespace should not change the config hash: %q vs %q", a, spaced)
	}

	// A genuinely different config must still hash differently.
	if a == hashSectionConfig(json.RawMessage(`{"sort":"title","order":"desc"}`)) {
		t.Fatalf("distinct configs must hash differently")
	}

	// Invalid JSON falls back to raw bytes rather than collapsing together.
	if hashSectionConfig(json.RawMessage(`not json`)) == hashSectionConfig(json.RawMessage(`also not`)) {
		t.Fatalf("distinct non-JSON configs must hash differently")
	}
}

// TestResolvedListCacheDoesNotCacheEmpty verifies a transiently empty result is
// not frozen for the TTL: the loader runs again on the next request.
func TestResolvedListCacheDoesNotCacheEmpty(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	key := "empty-not-cached"
	now := time.Unix(1_700_000_000, 0)

	var calls int64
	emptyLoader := func(context.Context) ([]*models.MediaItem, int, error) {
		atomic.AddInt64(&calls, 1)
		return nil, 0, nil
	}

	if _, _, err := getOrRefresh(context.Background(), key, now, emptyLoader); err != nil {
		t.Fatalf("first empty load: %v", err)
	}
	// A second read in the same fresh window must rebuild (empty was not cached).
	got, _, err := getOrRefresh(context.Background(), key, now, staticLoader(mediaItems("now-has-content"), &calls))
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if ids := itemIDs(got); len(ids) != 1 || ids[0] != "now-has-content" {
		t.Fatalf("second load returned %v, want [now-has-content]", ids)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("loader invoked %d times, want 2 (empty result was not cached)", got)
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

// TestIsCacheableSectionType covers the whitelist, the user-collection
// exclusion, and the explicit exclusions. Cacheability is derived from
// userAgnosticSectionFetcher, so this doubles as a pin on that table.
func TestIsCacheableSectionType(t *testing.T) {
	f := &Fetcher{}
	cacheable := []SectionType{
		SectionRecentlyAdded,
		SectionRecentlyReleased,
		SectionGenre,
		SectionCustomFilter,
		SectionCriticallyAcclaimed,
		SectionAwardWinners,
		SectionFormatShowcase,
		SectionSeasonalThemed,
		SectionMoodCollection,
		SectionTrendingOnServer,
		SectionNewToLibrary,
		SectionMostWatched,
		SectionTrendingDiscover,
		SectionAdminCuratedList,
	}
	for _, st := range cacheable {
		if !f.isCacheableSectionType(ResolvedSection{SectionType: st}) {
			t.Errorf("expected %s to be cacheable", st)
		}
	}

	notCacheable := []SectionType{
		SectionRandom,
		SectionContinueWatching,
		SectionNextUp,
		SectionNextInSeries,
		SectionRecommendedForYou,
		SectionBecauseYouWatched,
		SectionSimilarUsersLiked,
		SectionTasteMatch,
		SectionHiddenGems,
		SectionForgottenFavorites,
		SectionProfileActivityFeed,
		SectionEditorialSpotlight,
	}
	for _, st := range notCacheable {
		if f.isCacheableSectionType(ResolvedSection{SectionType: st}) {
			t.Errorf("expected %s to NOT be cacheable", st)
		}
	}
}

// TestIsCacheableSectionTypePersonalizedFilter verifies genre / custom_filter
// sections whose QueryDefinition carries a personalized (per-profile) rule or
// sort are NOT cacheable — their membership differs per profile and the shared
// cache key excludes userID/profileID — while non-personalized definitions of
// the same types stay cacheable.
func TestIsCacheableSectionTypePersonalizedFilter(t *testing.T) {
	f := &Fetcher{}
	nonPersonalized := mustJSON(t, catalog.QueryDefinition{
		Match: "all",
		Groups: []catalog.QueryGroup{
			{Match: "all", Rules: []catalog.QueryRule{
				{Field: "genre", Op: "contains", Value: "Action"},
			}},
		},
	})

	personalizedRule := mustJSON(t, catalog.QueryDefinition{
		Match: "all",
		Groups: []catalog.QueryGroup{
			{Match: "all", Rules: []catalog.QueryRule{
				{Field: "genre", Op: "contains", Value: "Action"},
				{Field: "in_watchlist", Op: "is", Value: true},
			}},
		},
	})

	personalizedSort := mustJSON(t, catalog.QueryDefinition{
		Match: "all",
		Groups: []catalog.QueryGroup{
			{Match: "all", Rules: []catalog.QueryRule{
				{Field: "watched", Op: "is", Value: false},
			}},
		},
		Sort: catalog.QuerySort{Field: "progress", Order: "desc"},
	})

	for _, st := range []SectionType{SectionCustomFilter, SectionGenre} {
		if !f.isCacheableSectionType(ResolvedSection{SectionType: st, Config: nonPersonalized}) {
			t.Errorf("%s with a non-personalized definition should be cacheable", st)
		}
		if f.isCacheableSectionType(ResolvedSection{SectionType: st, Config: personalizedRule}) {
			t.Errorf("%s with a personalized rule (in_watchlist) must NOT be cacheable", st)
		}
		if f.isCacheableSectionType(ResolvedSection{SectionType: st, Config: personalizedSort}) {
			t.Errorf("%s with a personalized sort/rule (watched/progress) must NOT be cacheable", st)
		}
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

// TestResolvedListCacheRefreshAhead verifies an entry past refreshAfter but
// before expiry serves the current value without blocking on a slow loader, and
// eventually swaps in the refreshed value.
func TestResolvedListCacheRefreshAhead(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	key := "refresh-ahead"
	base := time.Unix(1_700_000_000, 0)

	// Prime a cold entry at base.
	if _, _, err := getOrRefresh(context.Background(), key, base, staticLoader(mediaItems("old"), nil)); err != nil {
		t.Fatalf("priming entry: %v", err)
	}

	// Slow refresh loader: blocks until released, then returns the fresh list
	// and signals completion.
	release := make(chan struct{})
	loaderReturned := make(chan struct{})
	slowLoader := func(context.Context) ([]*models.MediaItem, int, error) {
		<-release
		close(loaderReturned)
		return mediaItems("new"), 1, nil
	}

	// Now is past refreshAfter (base+12m) but before expiry (base+15m): serve
	// stale and trigger the async rebuild. This call must not block on the slow
	// loader.
	within := base.Add(13 * time.Minute)
	done := make(chan struct{})
	var served []*models.MediaItem
	go func() {
		items, _, err := getOrRefresh(context.Background(), key, within, slowLoader)
		if err != nil {
			t.Errorf("refresh-ahead read: %v", err)
		}
		served = items
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("refresh-ahead read blocked on the slow loader")
	}
	if got := itemIDs(served); len(got) != 1 || got[0] != "old" {
		t.Fatalf("refresh-ahead served %v, want [old] (the cached value)", got)
	}

	// Release the background rebuild and wait for it to finish.
	close(release)
	select {
	case <-loaderReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("background rebuild loader never completed")
	}

	// The refreshed value swaps in. Poll (the set happens just after the loader
	// returns) using the same within-window clock so the entry is served fresh.
	if !waitFor(2*time.Second, func() bool {
		items, _, err := getOrRefresh(context.Background(), key, within, func(context.Context) ([]*models.MediaItem, int, error) {
			return mediaItems("unexpected"), 1, nil
		})
		if err != nil {
			return false
		}
		ids := itemIDs(items)
		return len(ids) == 1 && ids[0] == "new"
	}) {
		t.Fatalf("refreshed value never swapped in")
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
// disconnecting must not fail all the other requests riding on the flight.
func TestBlockingRebuildDetachedFromLeaderCancellation(t *testing.T) {
	resetResolvedListCacheForTest()
	defer resetResolvedListCacheForTest()

	// A context canceled BEFORE the build starts: without detachment the loader
	// (which honors ctx like the real fetchers do) would fail immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	loader := func(loadCtx context.Context) ([]*models.MediaItem, int, error) {
		if err := loadCtx.Err(); err != nil {
			return nil, 0, err
		}
		return mediaItems("x1"), 1, nil
	}

	now := time.Unix(1_700_000_000, 0)
	items, total, err := getOrRefresh(ctx, "detach-key", now, loader)
	if err != nil {
		t.Fatalf("blocking rebuild must run detached from the leader's cancellation, got %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ContentID != "x1" {
		t.Fatalf("unexpected result: items=%v total=%d", itemIDs(items), total)
	}

	// The successful detached build must have been cached for later requests.
	if _, ok := resolvedListGet("detach-key"); !ok {
		t.Fatal("detached rebuild did not cache its result")
	}
}
