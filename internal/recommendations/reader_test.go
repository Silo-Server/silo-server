package recommendations

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestGetBecauseYouWatchedWithSourceReturnsAnchorThatHasCache(t *testing.T) {
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

	items, sourceID, err := NewReader(repo, nil, nil, nil).GetBecauseYouWatchedWithSource(ctx, userID, profile, "", 10, catalog.AccessFilter{})
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
	if row := clusterRow(TasteCluster{}, nil); row.Label != "Because you enjoy For You" {
		t.Fatalf("unlabelled cluster title = %q", row.Label)
	}
}

type countingReadRefresher struct{ calls int }

func (c *countingReadRefresher) RequestReadRefresh(context.Context, int, string) { c.calls++ }

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
	items, source, err := r.GetBecauseYouWatchedWithSource(t.Context(), 7, "p1", "", 10, catalog.AccessFilter{})
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
// accessible unless listed in hidden.
type fakeReaderRepo struct {
	meta     *TasteProfileMeta
	clusters []TasteCluster
	// personal holds the profile's cached rows by rec type and source item,
	// global the global rows by rec type.
	personal map[string][]ScoredItem
	global   map[string][]ScoredItem
	hidden   map[string]struct{}
}

func (f *fakeReaderRepo) GetTasteProfileMeta(context.Context, int, string) (*TasteProfileMeta, error) {
	return f.meta, nil
}

func (f *fakeReaderRepo) GetTasteClusters(context.Context, int, string) ([]TasteCluster, error) {
	return f.clusters, nil
}

func (f *fakeReaderRepo) GetRecommendationCache(_ context.Context, userID int, _, recType, sourceItemID string) ([]ScoredItem, error) {
	if userID == GlobalCacheUserID {
		return f.global[recType], nil
	}
	return f.personal[recType+"|"+sourceItemID], nil
}

func (f *fakeReaderRepo) ListCachedGenreSamplers(context.Context) (map[string][]ScoredItem, error) {
	return nil, nil
}

func (f *fakeReaderRepo) GetTopGenres(context.Context, int) ([]string, error) {
	return nil, nil
}

func (f *fakeReaderRepo) FilterAccessibleItemIDs(_ context.Context, itemIDs []string, _ catalog.AccessFilter) (map[string]struct{}, error) {
	accessible := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		if _, hidden := f.hidden[id]; !hidden {
			accessible[id] = struct{}{}
		}
	}
	return accessible, nil
}

func rowLabels(rows []ForYouRow) []string {
	labels := make([]string, len(rows))
	for i, row := range rows {
		labels[i] = row.Label
	}
	return labels
}

// A profile with no positive signal, or no taste profile, is served the
// global rows only, even while personal rows from an earlier taste profile
// are still cached; positive signals bring the personal rows in by level.
func TestForYouPageServesGlobalRowsOnlyWithoutPositiveSignals(t *testing.T) {
	const (
		forYou  = "For You"
		drama   = "Because you enjoy Drama"
		popular = "Popular on This Server"
		recent  = "Recently Added"
	)
	for _, tc := range []struct {
		name string
		meta *TasteProfileMeta
		want []string
	}{
		{"no taste profile", nil, []string{popular, recent}},
		{"cleared taste vector", &TasteProfileMeta{SignalCounts: map[string]int{}}, []string{popular, recent}},
		{"only negative signals", &TasteProfileMeta{SignalCounts: map[string]int{"watch_low": 1, "rated_low": 2}}, []string{popular, recent}},
		{"one positive signal", &TasteProfileMeta{SignalCounts: map[string]int{"favorited": 1, "watch_low": 4}}, []string{popular, recent, forYou}},
		{"fully personalized", &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}}, []string{forYou, drama, popular, recent}},
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
