package recommendations

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A row title carries its catalog key only while it is worded as titles.go
// words it, for the row type that uses that wording.
func TestRowTitleKeys(t *testing.T) {
	const becauseYouEnjoy = "home.section.becauseYouEnjoy"
	for _, tc := range []struct {
		rowType, label, key string
		args                map[string]string
	}{
		{clusterRowType, "For You", "home.section.forYou", nil},
		{clusterRowType, "Because you enjoy Crime", becauseYouEnjoy, map[string]string{"genres": "Crime"}},
		// renameClusterRow adds a genre to tell two rows apart.
		{clusterRowType, "Because you enjoy Crime, Drama", becauseYouEnjoy, map[string]string{"genres": "Crime, Drama"}},
		{clusterRowType, "Picked from your history", "home.section.pickedFromYourHistory", nil},
		{RecTypeSimilarUsersLiked, "Profiles Like You Enjoyed", "home.section.profilesLikeYouEnjoyed", nil},
		{RecTypePopular, "Popular on This Server", "home.section.popularOnServer", nil},
		{RecTypeRecentlyAdded, "Recently Added", "home.section.recentlyAdded", nil},
		{RecTypeTopRated, "Highly Rated in Your Library", "home.section.highlyRatedInLibrary", nil},
		{genreSamplerRowType, "Top Drama", "home.section.topGenre", map[string]string{"genre": "Drama"}},
		// Other wording, such as a title cached by an older build, and the
		// right words on another row type carry no key.
		{clusterRowType, "Because you enjoy ", "", nil},
		{clusterRowType, "Because You Enjoy Crime", "", nil},
		{genreSamplerRowType, "Top ", "", nil},
		{genreSamplerRowType, "Popular in Crime", "", nil},
		{RecTypePopular, "For You", "", nil},
		{RecTypeBecauseWatched, "Because You Watched", "", nil},
	} {
		key, args := ForYouRow{Type: tc.rowType, Label: tc.label}.TitleKey()
		if key != tc.key || !maps.Equal(args, tc.args) {
			t.Errorf("%s %q: key %q %v, want %q %v", tc.rowType, tc.label, key, args, tc.key, tc.args)
		}
	}
}

type countingReadRefresher struct{ calls int }

func (c *countingReadRefresher) RequestReadRefresh(context.Context, int, string) { c.calls++ }

func (c *countingReadRefresher) ReadRefreshDue(int, string) bool { return true }

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
// accessible unless listed in hidden, its type in types is not one of
// recommendableMediaTypes or, when the filter names allowed libraries, its
// library in libraries is not one of them.
type fakeReaderRepo struct {
	meta     *TasteProfileMeta
	clusters []TasteCluster
	// personal holds the profile's cached rows by rec type and source item,
	// global the global rows by rec type.
	personal  map[string][]ScoredItem
	global    map[string][]ScoredItem
	hidden    map[string]struct{}
	libraries map[string]int
	// types holds item media types; an item missing from it is a movie.
	types map[string]string
	// genres holds the cached genre samplers by genre.
	genres map[string][]ScoredItem
	// defaults holds the live default rows by kind, best first;
	// defaultCalls records each read of one.
	defaults     map[string][]ScoredItem
	defaultCalls []defaultRowCall
	// rated holds the titles the profile rated.
	rated []string
}

func (f *fakeReaderRepo) ListRatedItemIDs(context.Context, int, string) ([]string, error) {
	return f.rated, nil
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

func (f *fakeReaderRepo) TopGenre(context.Context) (string, error) {
	return "", nil
}

func (f *fakeReaderRepo) FilterRecommendableItemIDs(_ context.Context, itemIDs []string, filter catalog.AccessFilter) (map[string]struct{}, error) {
	accessible := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		if _, hidden := f.hidden[id]; hidden {
			continue
		}
		if mediaType, ok := f.types[id]; ok && !slices.Contains(recommendableMediaTypes, mediaType) {
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
	accessible, _ := f.FilterRecommendableItemIDs(ctx, ids, filter)
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
			rows, err := r.getForYouPageRows(t.Context(), 7, "p1", catalog.AccessFilter{})
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
			// Popular scores an account count, at least one.
			RecTypePopular:                      {{MediaItemID: "popular", Score: 1}},
			RecTypeRecentlyAdded:                {{MediaItemID: "recent"}},
			RecTypeGenreSamplerPrefix + "Drama": {{MediaItemID: "top-drama"}},
		},
		defaults: map[string][]ScoredItem{RecTypeRecentlyAdded: {{MediaItemID: "recent"}}},
		genres:   map[string][]ScoredItem{"Drama": {{MediaItemID: "top-drama"}}},
	}
	r := (&Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}).WithPersonalRows(false)
	ctx := t.Context()

	rows, err := r.getForYouPageRows(ctx, 7, "p1", catalog.AccessFilter{})
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
	discover, err := r.GetDiscoverRows(ctx, 7, "p1", ServedRowSize, catalog.AccessFilter{})
	if err != nil || !slices.Contains(rowLabels(discover), genreRowLabel("Drama")) {
		t.Fatalf("discover = %v, %v; want the Drama genre row, not left out for the cached Drama taste", rowLabels(discover), err)
	}
	tonight, err := r.GetWatchTonight(ctx, 7, "p1", 20, catalog.AccessFilter{})
	if err != nil || !tonight.IsCold || !slices.Equal(scoredIDs(tonight.Items), []string{"popular", "recent"}) {
		t.Fatalf("watch tonight = %+v, %v; want the cold-start rows only", tonight, err)
	}
}

// Watch Tonight's cold-start fallback reads Recently Added live, so it still
// fills once the cached global rows expire, as they do with recommendations
// disabled and no worker rebuilding them; the query leaves out what the
// profile watched.
func TestWatchTonightFallsBackToTheLiveRecentlyAddedRow(t *testing.T) {
	repo := &fakeReaderRepo{
		defaults: map[string][]ScoredItem{RecTypeRecentlyAdded: {{MediaItemID: "watched", Score: 2}, {MediaItemID: "recent", Score: 1}}},
	}
	r := (&Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, watchedStore("watched")).WithUserStoreOutsidePostgres(true)}).WithPersonalRows(false)
	tonight, err := r.GetWatchTonight(t.Context(), 7, "p1", 8, catalog.AccessFilter{})
	if err != nil || !tonight.IsCold || !slices.Equal(scoredIDs(tonight.Items), []string{"recent"}) {
		t.Fatalf("watch tonight = %+v, %v; want the live Recently Added row", tonight, err)
	}
	if len(repo.defaultCalls) != 1 || repo.defaultCalls[0].kind != RecTypeRecentlyAdded || !slices.Equal(repo.defaultCalls[0].exclude, []string{"watched"}) {
		t.Fatalf("default row reads = %+v, want one Recently Added read leaving out the watched title", repo.defaultCalls)
	}
}

// Watch Tonight's cold-start list is the same on every read: equal scores
// keep the order their row holds them in, so trimming keeps the same titles,
// and Recently Added is scored by its rank, not its Unix-seconds score, so it
// interleaves with Popular across its band rather than all landing near 0.35.
func TestWatchTonightColdStartOrder(t *testing.T) {
	const day = 86400.0
	added := func(daysAgo float64) float64 { return 1.8e9 - daysAgo*day }
	for _, tc := range []struct {
		name            string
		popular, recent []ScoredItem
		limit           int
		want            []string
	}{
		{
			name: "Popular ties keep the cached order",
			// The Popular row is cached most accounts first, then most
			// recently watched, then by content ID.
			popular: []ScoredItem{{MediaItemID: "pa", Score: 2}, {MediaItemID: "pb", Score: 2}, {MediaItemID: "pc", Score: 2}, {MediaItemID: "pd", Score: 2}, {MediaItemID: "pe", Score: 2}},
			limit:   3,
			want:    []string{"pa", "pb", "pc"},
		},
		{
			name:    "Recently Added interleaves with Popular by rank",
			popular: []ScoredItem{{MediaItemID: "p5", Score: 5}, {MediaItemID: "p3", Score: 3}, {MediaItemID: "p1", Score: 1}},
			recent:  []ScoredItem{{MediaItemID: "r1", Score: added(0)}, {MediaItemID: "r2", Score: added(1)}, {MediaItemID: "r3", Score: added(2)}, {MediaItemID: "r4", Score: added(3)}},
			limit:   8,
			// Popular 0.40, 0.32, 0.24; Recently Added 0.35, 0.30, 0.25, 0.20.
			want: []string{"p5", "r1", "p3", "r2", "r3", "p1", "r4"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeReaderRepo{
				global:   map[string][]ScoredItem{RecTypePopular: tc.popular},
				defaults: map[string][]ScoredItem{RecTypeRecentlyAdded: tc.recent},
			}
			r := (&Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}).WithPersonalRows(false)
			// The candidates pass through a map, whose order changes per run.
			for range 20 {
				tonight, err := r.GetWatchTonight(t.Context(), 7, "p1", tc.limit, catalog.AccessFilter{})
				if err != nil {
					t.Fatal(err)
				}
				if got := scoredIDs(tonight.Items); !slices.Equal(got, tc.want) {
					t.Fatalf("watch tonight = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// fakeItemRatings serves a profile's rating scores by item from memory.
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
