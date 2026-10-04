package recommendations

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func scored(ids ...string) []ScoredItem {
	items := make([]ScoredItem, len(ids))
	for i, id := range ids {
		items[i] = ScoredItem{MediaItemID: id}
	}
	return items
}

func numbered(prefix string, n int) []ScoredItem {
	items := make([]ScoredItem, n)
	for i := range items {
		items[i] = ScoredItem{MediaItemID: fmt.Sprintf("%s%02d", prefix, i)}
	}
	return items
}

func rowItems(rows []ForYouRow, label string) []string {
	for _, row := range rows {
		if row.Label == label {
			return itemIDs(row.Items)
		}
	}
	return nil
}

// watchedStore is a user store in which profile p1 watched the given items.
func watchedStore(ids ...string) fakeSignalProvider {
	store := &fakeSignalStore{}
	for _, id := range ids {
		store.progress = append(store.progress, userstore.WatchProgress{ProfileID: "p1", MediaItemID: id, Completed: true})
	}
	return fakeSignalProvider{store: store}
}

// A new profile on a server that has cached nothing, with recommendations
// disabled (no refresher), still gets the default rows on Discover, queried
// without the titles it watched.
func TestDiscoverServesTheDefaultRowsWithNothingCached(t *testing.T) {
	repo := &fakeReaderRepo{defaults: map[string][]ScoredItem{
		RecTypeTopRated:      scored("watched", "rated-1", "rated-2"),
		RecTypeRecentlyAdded: scored("recent-1", "watched"),
	}}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, watchedStore("watched"))}

	rows, err := r.GetDiscoverRows(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowLabels(rows), []string{highlyRatedLabel, recentlyAddedLabel}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if got := rowItems(rows, highlyRatedLabel); !slices.Equal(got, []string{"rated-1", "rated-2"}) {
		t.Fatalf("highly rated = %v", got)
	}
	if rows[0].Type != RecTypeTopRated || rows[1].Type != RecTypeRecentlyAdded {
		t.Fatalf("row types = %q, %q", rows[0].Type, rows[1].Type)
	}
	for _, call := range repo.defaultCalls {
		if !slices.Equal(call.exclude, []string{"watched"}) || call.limit != CacheCandidateLimit {
			t.Fatalf("%s queried with exclude=%v limit=%d, want the watched title and a full pool", call.kind, call.exclude, call.limit)
		}
	}
}

// Discover serves the live Recently Added row in place of the cached one and
// adds Highly Rated, after the personal rows of a personalized profile.
// Home and library sections keep the cached rows and never query the live
// ones.
func TestDiscoverServesDefaultRowsAndSectionsDoNot(t *testing.T) {
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
		clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama"}},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                scored("main"),
			RecTypeForYouClusterPrefix + "0" + "|": {{MediaItemID: "drama", Reason: clusterTitle("Drama")}},
		},
		global: map[string][]ScoredItem{
			RecTypePopular:       scored("popular"),
			RecTypeRecentlyAdded: scored("cached-recent"),
			RecTypeTopRated:      scored("cached-top-rated"),
		},
		defaults: map[string][]ScoredItem{
			RecTypeTopRated:      scored("rated"),
			RecTypeRecentlyAdded: scored("recent"),
		},
	}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}

	rows, err := r.GetDiscoverRows(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowLabels(rows), []string{"For You", "Because you enjoy Drama", "Popular on This Server", highlyRatedLabel, recentlyAddedLabel}; !slices.Equal(got, want) {
		t.Fatalf("discover rows = %v, want %v", got, want)
	}
	if got := rowItems(rows, recentlyAddedLabel); !slices.Equal(got, []string{"recent"}) {
		t.Fatalf("recently added = %v, want the live row", got)
	}

	repo.defaultCalls = nil
	page, err := r.GetForYouPage(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowLabels(page), []string{"For You", "Because you enjoy Drama", "Popular on This Server", recentlyAddedLabel}; !slices.Equal(got, want) {
		t.Fatalf("for you page = %v, want %v", got, want)
	}
	if got := rowItems(page, recentlyAddedLabel); !slices.Equal(got, []string{"cached-recent"}) {
		t.Fatalf("for you page recently added = %v, want the cached row", got)
	}
	if _, err := r.SectionForYouMain(t.Context(), 7, "p1", catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	if len(repo.defaultCalls) != 0 {
		t.Fatalf("for you page and sections queried the live rows: %+v", repo.defaultCalls)
	}
}

// A default row's "see all" page is the same live row, longer: its first
// items are the ones Discover shows, and an excluded or inaccessible title
// is on neither.
func TestDefaultRowSectionsMatchDiscover(t *testing.T) {
	repo := &fakeReaderRepo{
		global: map[string][]ScoredItem{RecTypeTopRated: scored("cached-top-rated")},
		defaults: map[string][]ScoredItem{
			RecTypeTopRated:      append(scored("watched", "hidden"), numbered("rated-", 40)...),
			RecTypeRecentlyAdded: numbered("recent-", 40),
		},
		hidden: map[string]struct{}{"hidden": {}},
	}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, watchedStore("watched"))}

	discover, err := r.GetDiscoverRows(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for kind, label := range map[string]string{SectionKindTopRated: highlyRatedLabel, SectionKindRecentlyAdded: recentlyAddedLabel} {
		row, err := r.GetSection(t.Context(), 7, "p1", kind, "", 60, catalog.AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if row == nil || row.Label != label || len(row.Items) != 40 {
			t.Fatalf("%s section = %+v, want 40 items labeled %q", kind, row, label)
		}
		shown := rowItems(discover, label)
		if len(shown) != 20 || !slices.Equal(itemIDs(row.Items[:20]), shown) {
			t.Fatalf("%s section starts %v, discover shows %v", kind, itemIDs(row.Items[:20]), shown)
		}
		if slices.Contains(itemIDs(row.Items), "watched") || slices.Contains(itemIDs(row.Items), "hidden") {
			t.Fatalf("%s section shows an excluded or hidden title", kind)
		}
	}
}

// Discover dedupes a row only against the items earlier rows show: an item
// past an earlier row's limit still appears in a later row.
func TestDiscoverDedupesOnlyAgainstShownItems(t *testing.T) {
	main := numbered("main-", 25)
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
		personal: map[string][]ScoredItem{RecTypeForYouMain + "|": main},
		defaults: map[string][]ScoredItem{RecTypeTopRated: scored("main-03", "main-22", "rated")},
		genres:   map[string][]ScoredItem{"Drama": scored("main-21", "rated", "drama")},
	}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}

	rows, err := r.GetDiscoverRows(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowItems(rows, highlyRatedLabel); !slices.Equal(got, []string{"main-22", "rated"}) {
		t.Fatalf("highly rated = %v, want the unshown main-22 kept and the shown main-03 dropped", got)
	}
	// Genre rows are titled for their rating-led content.
	if got := rowItems(rows, "Top Drama"); !slices.Equal(got, []string{"main-21", "drama"}) {
		t.Fatalf("genre row = %v (rows %v)", got, rowLabels(rows))
	}
}

// A genre "see all" page carries the label Discover gives the row.
func TestGenreSectionIsTitledTopGenre(t *testing.T) {
	repo := &fakeReaderRepo{global: map[string][]ScoredItem{RecTypeGenreSamplerPrefix + "Drama": scored("drama")}}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}
	row, err := r.GetSection(t.Context(), 7, "p1", SectionKindGenre, "Drama", 60, catalog.AccessFilter{})
	if err != nil || row == nil || row.Label != "Top Drama" {
		t.Fatalf("genre section = %+v, %v; want Top Drama", row, err)
	}
}

// quietHouseholdCatalog is the global row store of a single-account server
// whose titles were all added a month ago: no title has a crowd of accounts
// behind it, and nothing was added in the last 14 days. cached holds what the
// worker writes.
type quietHouseholdCatalog struct {
	cached map[string][]ScoredItem
}

func (c *quietHouseholdCatalog) GetPopularItems(_ context.Context, _, minAccounts, _ int) ([]ScoredItem, error) {
	if minAccounts > 1 {
		return nil, nil
	}
	return scored("household-pick"), nil
}

func (c *quietHouseholdCatalog) GetRecentlyAddedItems(_ context.Context, days, _ int) ([]ScoredItem, error) {
	if days > 0 && days < 30 {
		return nil, nil
	}
	return scored("month-old-1", "month-old-2"), nil
}

func (c *quietHouseholdCatalog) GetTopGenres(context.Context, int) ([]string, error) {
	return nil, nil
}

func (c *quietHouseholdCatalog) GetGenreSamplerItems(context.Context, string, int) ([]ScoredItem, error) {
	return nil, nil
}

func (c *quietHouseholdCatalog) UpsertRecommendationCache(_ context.Context, _ int, _, recType, _ string, items []ScoredItem, _ string) error {
	c.cached[recType] = items
	return nil
}

func (c *quietHouseholdCatalog) ExtendGlobalRecommendationCache(context.Context, string, bool, string) (int64, error) {
	return 0, nil
}

func (c *quietHouseholdCatalog) DeleteGlobalRecommendationCache(context.Context, string, bool, []string) (int64, error) {
	return 0, nil
}

// On a single-account server where nothing was added in the last 14 days,
// a brand-new profile's cached reads (home and library "Recommended for You",
// v1 For You, Jellyfin, Watch Tonight) still get a row: the worker caches
// Recently Added without a window, and it stands in for the absent Popular
// row.
func TestNewProfileGetsARowOnAQuietSingleAccountServer(t *testing.T) {
	ctx := t.Context()
	store := &quietHouseholdCatalog{cached: map[string][]ScoredItem{}}
	newJobTestWorker(&fakeLocker{}, nil).cacheGlobalRows(ctx, store, "expiry")
	if _, ok := store.cached[RecTypePopular]; ok {
		t.Fatal("one account's viewing was cached as Popular")
	}
	r := &Reader{repo: &fakeReaderRepo{global: store.cached}, signals: NewSignalReader(&fakeSignalRepo{}, nil)}
	want := []string{"month-old-1", "month-old-2"}

	main, err := r.GetForYouMain(ctx, 7, "p1", 20, catalog.AccessFilter{})
	if err != nil || main == nil || main.Label != recentlyAddedLabel || !slices.Equal(itemIDs(main.Items), want) {
		t.Fatalf("for you main = %+v, %v; want the Recently Added row", main, err)
	}
	home, err := r.SectionForYouMain(ctx, 7, "p1", catalog.AccessFilter{})
	if err != nil || home == nil || !slices.Equal(itemIDs(home.Items), want) {
		t.Fatalf("home row = %+v, %v; want the Recently Added row", home, err)
	}
	page, err := r.GetForYouPage(ctx, 7, "p1", 20, catalog.AccessFilter{})
	if err != nil || len(page) == 0 {
		t.Fatalf("for you page = %v, %v; want a row", rowLabels(page), err)
	}
	tonight, err := r.GetWatchTonight(ctx, 7, "p1", 20, catalog.AccessFilter{})
	if err != nil || !tonight.IsCold || len(tonight.Items) != len(want) {
		t.Fatalf("watch tonight = %+v, %v; want the cold-start titles", tonight, err)
	}
}
