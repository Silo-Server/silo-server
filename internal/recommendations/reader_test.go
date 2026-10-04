package recommendations

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestSectionBecauseYouWatchedReturnsAnchorThatHasCache(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tbcw-source-"
	const profile = "66200000-0000-4000-8000-000000000001"
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'anchor test')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	cleanupRecoMediaItems(t, pool, prefix)
	latest, older, rec := prefix+"latest", prefix+"older", prefix+"rec"
	for _, id := range []string{latest, older, rec} {
		seedRecoMediaItem(t, pool, id, "movie", "matched")
	}
	for i, id := range []string{latest, older} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed,updated_at) VALUES($1,$2,$3,true,TIMESTAMPTZ '2026-08-10 12:00:00Z' - $4 * INTERVAL '1 hour')`, userID, profile, id, i); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepo(pool)
	// Only the older watch has a cached row; the latest watch must be skipped.
	expires := time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := repo.UpsertRecommendationCache(ctx, userID, profile, RecTypeBecauseWatched, older, []ScoredItem{{MediaItemID: rec, Score: 1}}, expires); err != nil {
		t.Fatal(err)
	}

	items, sourceID, err := NewReader(repo, nil, nil, nil).SectionBecauseYouWatched(ctx, userID, profile, "", nil, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != older {
		t.Fatalf("source = %q, want %q", sourceID, older)
	}
	if len(items) != 1 || items[0].MediaItemID != rec {
		t.Fatalf("items = %+v, want only %q", items, rec)
	}
}

func TestClusterRowTitleComesFromTheCachedRow(t *testing.T) {
	// The taste job renumbered the clusters: index 0 is now Horror, but the
	// cached row for index 0 still holds the Comedy build.
	horror := TasteCluster{ClusterIdx: 0, Label: "Horror"}
	cached := []ScoredItem{{MediaItemID: "m1", Reason: clusterTitle("Comedy")}, {MediaItemID: "m2", Reason: clusterTitle("Comedy")}}
	if row := clusterRow(horror, cached); row.Label != "Because you enjoy Comedy" || row.ClusterIndex != 0 || len(row.Items) != 2 {
		t.Fatalf("row = %+v, want the cached Comedy title", row)
	}

	// Rows whose items carry no cluster title use the cluster label.
	for _, reason := range []string{"", "cluster_match", clusterTitlePrefix} {
		row := clusterRow(horror, []ScoredItem{{MediaItemID: "m1", Reason: reason}})
		if row.Label != "Because you enjoy Horror" {
			t.Fatalf("reason %q: title = %q", reason, row.Label)
		}
	}
	// An unlabeled cluster is not "Because you enjoy" anything, and its title
	// round-trips through the cache like a labeled one.
	if row := clusterRow(TasteCluster{}, nil); row.Label != unlabeledClusterTitle {
		t.Fatalf("unlabeled cluster title = %q", row.Label)
	}
	if row := clusterRow(horror, []ScoredItem{{MediaItemID: "m1", Reason: clusterTitle("")}}); row.Label != unlabeledClusterTitle || row.Subject != "" {
		t.Fatalf("cached unlabeled title = %q (subject %q), want %q", row.Label, row.Subject, unlabeledClusterTitle)
	}
}

type countingReadRefresher struct{ calls int }

func (c *countingReadRefresher) RequestReadRefresh(context.Context, int, string) { c.calls++ }

func (c *countingReadRefresher) ReadRefreshDue(int, string) bool { return true }

// A read of a profile with no taste profile asks for a refresh only when the
// profile has signals to build one from.
func TestReaderRefreshesAProfileWithoutTasteOnlyWhenItHasSignals(t *testing.T) {
	ctx := t.Context()
	refresher := &countingReadRefresher{}
	r := &Reader{refresh: refresher, signals: NewSignalReader(&fakeSignalRepo{signalRows: true}, nil)}
	r.requestRefreshIfSignals(ctx, 7, "p1")
	if refresher.calls != 1 {
		t.Fatalf("refreshes with signals = %d, want 1", refresher.calls)
	}

	r.signals = NewSignalReader(&fakeSignalRepo{}, nil)
	r.requestRefreshIfSignals(ctx, 7, "p1")
	if refresher.calls != 1 {
		t.Fatalf("refreshes without signals = %d, want none", refresher.calls-1)
	}

	// Without a refresher (recommendations disabled) it does not even look.
	repo := &fakeSignalRepo{signalRows: true}
	(&Reader{signals: NewSignalReader(repo, nil)}).requestRefreshIfSignals(ctx, 7, "p1")
	if len(repo.signalRowsCalls) != 0 {
		t.Fatalf("signal checks without a refresher = %d, want none", len(repo.signalRowsCalls))
	}
}

// Because You Watched rows are built from completed titles, so a profile
// with none has nothing a refresh could add.
func TestBecauseYouWatchedWithoutCompletionsRequestsNoRefresh(t *testing.T) {
	refresher := &countingReadRefresher{}
	r := &Reader{refresh: refresher, signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: &fakeSignalStore{}})}
	items, source, err := r.SectionBecauseYouWatched(t.Context(), 7, "p1", "", nil, catalog.AccessFilter{})
	if err != nil || len(items) != 0 || source != "" {
		t.Fatalf("items=%v source=%q err=%v, want an empty row", items, source, err)
	}
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none", refresher.calls)
	}
}

// For You on a profile with no taste profile: a profile with no signals never
// queues a refresh; once it has one, the first read queues a refresh and
// later reads within the throttle interval do not queue another.
func TestForYouReadRefreshesNewProfilesWithSignalsOncePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tforyou-heal-"
	const profile = "66200000-0000-4000-8000-000000000002"
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'heal test')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	cleanupRecoMediaItems(t, pool, prefix)

	w, _ := newRefreshTestWorker()
	r := NewReader(NewRepo(pool), nil, w, nil)
	read := func() {
		t.Helper()
		if _, err := r.GetForYouMain(ctx, userID, profile, 20, catalog.AccessFilter{UserID: userID, ProfileID: profile}); err != nil {
			t.Fatal(err)
		}
	}

	read()
	read()
	assertNothingQueued(t, w, "profile without signals")

	watched := prefix + "movie"
	seedRecoMediaItem(t, pool, watched, "movie", "matched")
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed,updated_at) VALUES($1,$2,$3,true,NOW())`, userID, profile, watched); err != nil {
		t.Fatal(err)
	}
	read()
	if req := takeQueued(t, w); req.userID != userID || req.profileID != profile {
		t.Fatalf("queued %+v", req)
	}
	read()
	if _, err := r.GetDiscoverRows(ctx, userID, profile, 20, catalog.AccessFilter{UserID: userID, ProfileID: profile}); err != nil {
		t.Fatal(err)
	}
	assertNothingQueued(t, w, "reads within the throttle interval")
}

// fakeReaderRepo serves the Reader's reads from memory. Every item is
// accessible unless listed in hidden or, when the filter names allowed
// libraries, its library in libraries is not one of them.
type fakeReaderRepo struct {
	meta     *TasteProfileMeta
	clusters []TasteCluster
	// personal holds the profile's cached rows by rec type and source item,
	// global the global rows by rec type.
	personal  map[string][]ScoredItem
	global    map[string][]ScoredItem
	hidden    map[string]struct{}
	libraries map[string]int
	// genres holds the cached genre samplers by genre.
	genres map[string][]ScoredItem
	// defaults holds the live default rows by kind, best first;
	// defaultCalls records each read of one.
	defaults     map[string][]ScoredItem
	defaultCalls []defaultRowCall
}

type defaultRowCall struct {
	kind    string
	exclude []string
	limit   int
}

func (f *fakeReaderRepo) GetTasteProfileMeta(context.Context, int, string) (*TasteProfileMeta, error) {
	return f.meta, nil
}

func (f *fakeReaderRepo) GetTasteClusterMeta(context.Context, int, string) ([]TasteCluster, error) {
	return f.clusters, nil
}

func (f *fakeReaderRepo) GetRecommendationCache(_ context.Context, userID int, _, recType, sourceItemID string) ([]ScoredItem, error) {
	if userID == GlobalCacheUserID {
		return f.global[recType], nil
	}
	return f.personal[recType+"|"+sourceItemID], nil
}

func (f *fakeReaderRepo) ListCachedGenreSamplers(context.Context) (map[string][]ScoredItem, error) {
	return f.genres, nil
}

func (f *fakeReaderRepo) GetTopGenres(context.Context, int) ([]string, error) {
	return nil, nil
}

func (f *fakeReaderRepo) FilterAccessibleItemIDs(_ context.Context, itemIDs []string, filter catalog.AccessFilter) (map[string]struct{}, error) {
	accessible := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		if _, hidden := f.hidden[id]; hidden {
			continue
		}
		if filter.AllowedLibraryIDs != nil && !slices.Contains(filter.AllowedLibraryIDs, f.libraries[id]) {
			continue
		}
		accessible[id] = struct{}{}
	}
	return accessible, nil
}

// ListDefaultRowItems serves a default row as the query does: the accessible
// items not in exclude, at most limit of them.
func (f *fakeReaderRepo) ListDefaultRowItems(ctx context.Context, filter catalog.AccessFilter, kind string, exclude []string, limit int) ([]ScoredItem, error) {
	f.defaultCalls = append(f.defaultCalls, defaultRowCall{kind: kind, exclude: slices.Sorted(slices.Values(exclude)), limit: limit})
	var ids []string
	for _, item := range f.defaults[kind] {
		ids = append(ids, item.MediaItemID)
	}
	accessible, _ := f.FilterAccessibleItemIDs(ctx, ids, filter)
	items := []ScoredItem{}
	for _, item := range f.defaults[kind] {
		if _, ok := accessible[item.MediaItemID]; ok && !slices.Contains(exclude, item.MediaItemID) && len(items) < limit {
			items = append(items, item)
		}
	}
	return items, nil
}

func rowLabels(rows []ForYouRow) []string {
	labels := make([]string, len(rows))
	for i, row := range rows {
		labels[i] = row.Label
	}
	return labels
}

// A profile with no positive title, or no taste profile, is served the
// global rows only, even while personal rows from an earlier taste profile
// are still cached; positive titles bring the personal rows in by level. A
// profile seeded with three picks is level 2 and opens on its own row.
func TestForYouPageServesGlobalRowsOnlyWithoutPositiveSignals(t *testing.T) {
	const (
		forYou  = "For You"
		drama   = "Because you enjoy Drama"
		popular = "Popular on This Server"
		recent  = "Recently Added"
	)
	counts := func(kv ...any) *TasteProfileMeta {
		m := map[string]int{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1].(int)
		}
		return &TasteProfileMeta{SignalCounts: m}
	}
	for _, tc := range []struct {
		name string
		meta *TasteProfileMeta
		want []string
	}{
		{"no taste profile", nil, []string{popular, recent}},
		{"cleared taste vector", counts(signalCountPositiveTitles, 0), []string{popular, recent}},
		{"only negative signals", counts("watch_low", 1, "rated_low", 2, signalCountPositiveTitles, 0), []string{popular, recent}},
		{"watchlist only", counts("watchlist", 3, signalCountPositiveTitles, 0), []string{popular, recent}},
		{"one positive title", counts("favorited", 1, "watch_low", 4, signalCountPositiveTitles, 1), []string{popular, recent, forYou}},
		{"one title rated, favorited and finished", counts("rated_5", 1, "favorited", 1, "watch_high", 1, signalCountPositiveTitles, 1), []string{popular, recent, forYou}},
		{"three taste-seed picks", counts("favorited", 3, signalCountPositiveTitles, 3), []string{forYou, popular, drama, recent}},
		{"nine titles", counts("watch_high", 9, signalCountPositiveTitles, 9), []string{forYou, popular, drama, recent}},
		{"fully personalized", counts("watch_high", 12, signalCountPositiveTitles, ColdStartFullPersonalized), []string{forYou, drama, popular, recent}},
		{"legacy row with one positive signal", counts("favorited", 1, "watch_low", 4), []string{popular, recent, forYou}},
		{"legacy row past the top threshold", counts("watch_high", ColdStartFullPersonalized), []string{forYou, drama, popular, recent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeReaderRepo{
				meta:     tc.meta,
				clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama"}},
				personal: map[string][]ScoredItem{
					RecTypeForYouMain + "|":                {{MediaItemID: "personal-main"}},
					RecTypeForYouClusterPrefix + "0" + "|": {{MediaItemID: "personal-drama", Reason: clusterTitle("Drama")}},
				},
				global: map[string][]ScoredItem{
					RecTypePopular:       {{MediaItemID: "popular"}},
					RecTypeRecentlyAdded: {{MediaItemID: "recent"}},
				},
			}
			r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}
			rows, err := r.getForYouPageRows(t.Context(), 7, "p1", ServedRowSize, catalog.AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if got := rowLabels(rows); !slices.Equal(got, tc.want) {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
			main, err := r.GetForYouMain(t.Context(), 7, "p1", 20, catalog.AccessFilter{})
			if err != nil || main == nil || main.Label != tc.want[0] {
				t.Fatalf("main row = %+v, %v; want %q", main, err, tc.want[0])
			}
		})
	}
}

// With recommendations disabled a fully personalized profile is served the
// global rows only, though its personal rows are still cached, and no
// personal row is read for a section or Discover.
func TestDisabledRecommendationsServeNoPersonalRows(t *testing.T) {
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": 12, signalCountPositiveTitles: ColdStartFullPersonalized}},
		clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama", DominantGenres: []string{"Drama"}}},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                {{MediaItemID: "personal-main"}},
			RecTypeForYouClusterPrefix + "0" + "|": {{MediaItemID: "personal-drama", Reason: clusterTitle("Drama")}},
			RecTypeSimilarUsersLiked + "|":         {{MediaItemID: "peer-pick"}},
		},
		global: map[string][]ScoredItem{
			RecTypePopular:                      {{MediaItemID: "popular"}},
			RecTypeRecentlyAdded:                {{MediaItemID: "recent"}},
			RecTypeGenreSamplerPrefix + "Drama": {{MediaItemID: "top-drama"}},
		},
	}
	r := (&Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}).WithPersonalRows(false)
	ctx := t.Context()

	rows, err := r.getForYouPageRows(ctx, 7, "p1", ServedRowSize, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowLabels(rows); !slices.Equal(got, []string{"Popular on This Server", "Recently Added"}) {
		t.Fatalf("rows = %v, want the global rows only", got)
	}
	if items, err := r.GetSimilarUsersLiked(ctx, 7, "p1", 20, catalog.AccessFilter{}); err != nil || len(items) != 0 {
		t.Fatalf("similar users = %v, %v; want none", items, err)
	}
	if items, anchor, err := r.SectionBecauseYouWatched(ctx, 7, "p1", "", nil, catalog.AccessFilter{}); err != nil || len(items) != 0 || anchor != "" {
		t.Fatalf("because you watched = %v %q, %v; want none", items, anchor, err)
	}
	if row, err := r.SectionTasteMatchRow(ctx, 7, "p1", "Drama", catalog.AccessFilter{}); err != nil || row == nil || row.Type != genreSamplerRowType {
		t.Fatalf("taste match = %+v, %v; want the global genre row", row, err)
	}
	if row, err := r.loadSectionRow(ctx, 7, "p1", SectionKindForYouMain, ""); err != nil || row != nil {
		t.Fatalf("for you section = %+v, %v; want none", row, err)
	}
}

// A profile with positive signals but no personal rows asks for them even at
// level 0, since its titles may have gained embeddings since its last
// refresh; a profile with nothing positive, or with its rows, does not.
func TestForYouPageAsksForMissingRowsOnlyWithPositiveSignals(t *testing.T) {
	for _, tc := range []struct {
		name        string
		counts      map[string]int
		rowsCached  bool
		wantRefresh int
	}{
		{"picks awaiting embeddings", map[string]int{"favorited": 3, signalCountPositiveTitles: 0}, false, 1},
		{"level 2 without rows", map[string]int{"favorited": 3, signalCountPositiveTitles: 3}, false, 1},
		{"level 2 with rows", map[string]int{"favorited": 3, signalCountPositiveTitles: 3}, true, 0},
		{"watchlist only with rows", map[string]int{"watchlist": 2, signalCountPositiveTitles: 0}, true, 0},
		{"only negative signals", map[string]int{"rated_low": 1, "watch_low": 2, signalCountPositiveTitles: 0}, false, 0},
		{"only 3-star ratings", map[string]int{"rated_3": 2, signalCountPositiveTitles: 0}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeReaderRepo{
				meta:   &TasteProfileMeta{SignalCounts: tc.counts},
				global: map[string][]ScoredItem{RecTypePopular: {{MediaItemID: "popular"}}},
			}
			if tc.rowsCached {
				repo.personal = map[string][]ScoredItem{RecTypeForYouMain + "|": {{MediaItemID: "personal-main"}}}
			}
			refresher := &countingReadRefresher{}
			r := &Reader{repo: repo, refresh: refresher, signals: NewSignalReader(&fakeSignalRepo{}, nil)}
			if _, err := r.getForYouPageRows(t.Context(), 7, "p1", ServedRowSize, catalog.AccessFilter{}); err != nil {
				t.Fatal(err)
			}
			if refresher.calls != tc.wantRefresh {
				t.Fatalf("refresh requests = %d, want %d", refresher.calls, tc.wantRefresh)
			}
		})
	}
}

// filterRows leaves out what the profile watched or favorited, episodes
// counting for their series, so rows cached before either happened are
// cleaned. Watchlist titles stay; another profile's favorites do not count.
func TestFilterRowsDropsWatchedAndFavoritedTitles(t *testing.T) {
	store := &fakeSignalStore{
		progress: []userstore.WatchProgress{
			{ProfileID: "p1", MediaItemID: "movie-watched", Completed: true},
			{ProfileID: "p1", MediaItemID: "episode-watched", Completed: true},
		},
		favorites: []userstore.Favorite{
			{ProfileID: "p1", MediaItemID: "movie-seed-pick"},
			{ProfileID: "p1", MediaItemID: "episode-favorite"},
			{ProfileID: "p2", MediaItemID: "movie-plain"},
		},
		watchlist: []userstore.WatchlistEntry{{ProfileID: "p1", MediaItemID: "movie-watchlist"}},
	}
	signals := NewSignalReader(&fakeSignalRepo{canonical: map[string]string{
		"episode-watched":  "series-watched",
		"episode-favorite": "series-favorite",
	}}, fakeSignalProvider{store: store})
	r := &Reader{repo: &fakeReaderRepo{}, signals: signals}

	items := func(ids ...string) []ScoredItem {
		out := make([]ScoredItem, len(ids))
		for i, id := range ids {
			out[i] = ScoredItem{MediaItemID: id}
		}
		return out
	}
	rows, err := r.filterRows(t.Context(), 7, "p1", []ForYouRow{
		{Label: "For You", Items: items("movie-watched", "series-watched", "movie-seed-pick", "series-favorite", "movie-watchlist", "movie-plain")},
		{Label: "Because You Watched", Items: items("movie-seed-pick", "series-watched")},
	}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want only the For You row: the other held only excluded titles", rowLabels(rows))
	}
	var got []string
	for _, item := range rows[0].Items {
		got = append(got, item.MediaItemID)
	}
	if want := []string{"movie-watchlist", "movie-plain"}; !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
}

// fakeItemRatings serves a profile's star ratings by item from memory.
type fakeItemRatings map[string]int

func (f fakeItemRatings) ListForItems(_ context.Context, _ int, _ string, itemIDs []string) (map[string]int, error) {
	out := map[string]int{}
	for _, id := range itemIDs {
		if rating, ok := f[id]; ok {
			out[id] = rating
		}
	}
	return out, nil
}

// Anchors come from the latest ten completions, newest first, passing over
// those rated two stars or lower; a 3-star title still anchors.
func TestAnchorItemIDsPassOverDislikedCompletions(t *testing.T) {
	recent := []string{"one-star", "liked", "three-stars", "two-stars", "next"}
	for i := len(recent); i < 12; i++ {
		recent = append(recent, fmt.Sprintf("older-%d", i))
	}
	allLow := fakeItemRatings{}
	for _, id := range recent[:anchorCandidateLimit] {
		allLow[id] = 1
	}
	signals := NewSignalReader(&fakeSignalRepo{fallbackRecentCompleted: recent}, nil)
	for _, tc := range []struct {
		name    string
		ratings itemRatingReader
		want    []string
	}{
		{"no ratings reader", nil, recent[:3]},
		{"disliked passed over", fakeItemRatings{"one-star": 1, "two-stars": 2, "three-stars": 3, "liked": 5}, []string{"liked", "three-stars", "next"}},
		// Only the latest ten are looked at, so older liked titles do not
		// anchor a profile whose recent watching it disliked.
		{"latest ten all disliked", allLow, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := anchorItemIDs(t.Context(), signals, tc.ratings, 7, "p1", becauseYouWatchedAnchors)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("anchors = %v, want %v", got, tc.want)
			}
		})
	}
}

// A title the profile rated one star never heads a Because You Watched row,
// on Discover, in a home section or in Watch Tonight, even while the row
// built for it is still cached; the next completion heads the row instead.
func TestBecauseYouWatchedNeverOpensOnADislikedTitle(t *testing.T) {
	repo := &fakeReaderRepo{personal: map[string][]ScoredItem{
		RecTypeBecauseWatched + "|disliked": {{MediaItemID: "like-disliked", Score: 1}},
		RecTypeBecauseWatched + "|liked":    {{MediaItemID: "like-liked", Score: 1}},
		RecTypeBecauseWatched + "|older":    {{MediaItemID: "like-older", Score: 1}},
	}}
	refresher := &countingReadRefresher{}
	r := &Reader{
		repo:        repo,
		ratingsRepo: fakeItemRatings{"disliked": 1},
		refresh:     refresher,
		signals:     NewSignalReader(&fakeSignalRepo{fallbackRecentCompleted: []string{"disliked", "liked", "older"}}, nil),
	}
	ctx := t.Context()

	rows, err := r.GetBecauseYouWatchedRows(ctx, 7, "p1", 3, 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var anchors []string
	for _, row := range rows {
		anchors = append(anchors, row.AnchorItemID)
	}
	if want := []string{"liked", "older"}; !slices.Equal(anchors, want) {
		t.Fatalf("row anchors = %v, want %v", anchors, want)
	}

	_, source, err := r.SectionBecauseYouWatched(ctx, 7, "p1", "", nil, catalog.AccessFilter{})
	if err != nil || source != "liked" {
		t.Fatalf("section anchor = %q, %v; want liked", source, err)
	}

	tonight, err := r.GetWatchTonight(ctx, 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tonight.Items {
		if item.MediaItemID == "like-disliked" {
			t.Fatalf("Watch Tonight %v holds the disliked anchor's neighbor", tonight.Items)
		}
	}
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none: the remaining anchors have rows", refresher.calls)
	}

	// Without ratings the disliked title anchors as before.
	r.ratingsRepo = nil
	if _, source, _ := r.SectionBecauseYouWatched(ctx, 7, "p1", "", nil, catalog.AccessFilter{}); source != "disliked" {
		t.Fatalf("anchor without ratings = %q, want disliked", source)
	}
}

// Two cluster rows never share a title. A lighter row that mostly repeats a
// heavier one's served items is hidden, without asking for a rebuild; one
// with other items is named with its next dominant genre. A row whose
// cluster was renumbered since it was cached keeps its title.
func TestClusterRowsWithOneTitleAreToldApartOrHidden(t *testing.T) {
	titled := func(label string, items []ScoredItem) []ScoredItem {
		for i := range items {
			items[i].Reason = clusterTitle(label)
		}
		return items
	}
	heavy := titled("Family", scoredRun("f", 20))
	// 15 of 20 shared: a Jaccard index of 15/25.
	repeat := titled("Family", append(scoredRun("f", 15), scoredRun("r", 5)...))
	other := titled("Family", scoredRun("o", 20))
	stale := titled("Horror", scoredRun("h", 20))
	repo := &fakeReaderRepo{
		meta: &TasteProfileMeta{SignalCounts: map[string]int{signalCountPositiveTitles: ColdStartFullPersonalized}},
		clusters: []TasteCluster{
			{ClusterIdx: 0, Label: "Family", DominantGenres: []string{"Family", "Animation"}, TotalWeight: 2},
			{ClusterIdx: 1, Label: "Family", DominantGenres: []string{"Family", "Animation"}, TotalWeight: 3},
			{ClusterIdx: 2, Label: "Family", DominantGenres: []string{"Family", "Comedy", "Fantasy"}, TotalWeight: 1},
			// The table now labels index 3 "Drama", but its cached row is
			// the Horror build, so it cannot be renamed from the table.
			{ClusterIdx: 3, Label: "Drama", DominantGenres: []string{"Drama", "Crime"}, TotalWeight: 0.5},
			{ClusterIdx: 4, Label: "Horror", DominantGenres: []string{"Horror", "Thriller"}, TotalWeight: 0.8},
		},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                scoredRun("main", 20),
			RecTypeForYouClusterPrefix + "0" + "|": repeat,
			RecTypeForYouClusterPrefix + "1" + "|": heavy,
			RecTypeForYouClusterPrefix + "2" + "|": other,
			RecTypeForYouClusterPrefix + "3" + "|": stale,
			RecTypeForYouClusterPrefix + "4" + "|": titled("Horror", scoredRun("h2", 20)),
		},
	}
	refresher := &countingReadRefresher{}
	r := &Reader{repo: repo, refresh: refresher, signals: NewSignalReader(&fakeSignalRepo{}, nil)}

	rows, err := r.GetForYouPage(t.Context(), 7, "p1", ServedRowSize, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ForYouLabel, "Because you enjoy Family", "Because you enjoy Family, Comedy", "Because you enjoy Horror", "Because you enjoy Horror"}
	if got := rowLabels(rows); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if rows[1].ClusterIndex != 1 {
		t.Fatalf("kept Family row is cluster %d, want the heavier 1", rows[1].ClusterIndex)
	}
	renamed := rows[2]
	if renamed.Subject != "Family, Comedy" || renamed.Items[0].Reason != renamed.Label {
		t.Fatalf("renamed row subject %q, item reason %q", renamed.Subject, renamed.Items[0].Reason)
	}
	if other[0].Reason != clusterTitle("Family") {
		t.Fatal("renaming changed the cached items")
	}
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none: a hidden row was built", refresher.calls)
	}
}
