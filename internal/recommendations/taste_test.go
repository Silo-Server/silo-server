package recommendations

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Watch and rewatch signals count once per canonical title, in the bucket of
// the title's strongest watch, so episode rows do not inflate the cold-start
// level.
func TestCountWatchSignalsCountsEachTitleOnce(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	refs := map[string]canonicalContentRef{
		"movie-done":      {Kind: canonicalKindMovie, CanonicalID: "movie-done"},
		"movie-half":      {Kind: canonicalKindMovie, CanonicalID: "movie-half"},
		"movie-abandoned": {Kind: canonicalKindMovie, CanonicalID: "movie-abandoned"},
		"movie-paused":    {Kind: canonicalKindMovie, CanonicalID: "movie-paused"},
	}
	episodes := func(series string, n int) []string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("%s-e%02d", series, i)
			refs[ids[i]] = canonicalContentRef{Kind: canonicalKindEpisode, CanonicalID: series, SeriesID: series, SeasonNumber: 1, HasSeason: true}
		}
		return ids
	}
	binged := episodes("series-binged", 15)
	sampled := episodes("series-sampled", 3)
	dropped := episodes("series-dropped", 2)

	for _, tc := range []struct {
		name      string
		progress  []WatchProgressRow
		rewatches []RewatchCount
		want      map[string]int
	}{
		{
			name: "fifteen finished episodes are one title",
			progress: func() []WatchProgressRow {
				rows := make([]WatchProgressRow, 0, len(binged))
				for _, id := range binged {
					rows = append(rows, WatchProgressRow{MediaItemID: id, Completed: true, UpdatedAt: now})
				}
				return rows
			}(),
			want: map[string]int{"watch_high": 1},
		},
		{
			name: "a series lands in its strongest bucket",
			progress: []WatchProgressRow{
				{MediaItemID: sampled[0], PositionSeconds: 5, DurationSeconds: 100, UpdatedAt: old},
				{MediaItemID: sampled[1], PositionSeconds: 60, DurationSeconds: 100, UpdatedAt: now},
				{MediaItemID: sampled[2], PositionSeconds: 70, DurationSeconds: 100, UpdatedAt: now},
			},
			want: map[string]int{"watch_med": 1},
		},
		{
			name: "movies and series side by side",
			progress: []WatchProgressRow{
				{MediaItemID: "movie-done", Completed: true, UpdatedAt: now},
				{MediaItemID: "movie-half", PositionSeconds: 55, DurationSeconds: 100, UpdatedAt: now},
				{MediaItemID: "movie-abandoned", PositionSeconds: 5, DurationSeconds: 100, UpdatedAt: old},
				{MediaItemID: "movie-paused", PositionSeconds: 5, DurationSeconds: 100, UpdatedAt: now},
				{MediaItemID: dropped[0], PositionSeconds: 3, DurationSeconds: 100, UpdatedAt: old},
				{MediaItemID: dropped[1], PositionSeconds: 4, DurationSeconds: 100, UpdatedAt: old},
				{MediaItemID: "unknown", Completed: true, UpdatedAt: now},
			},
			want: map[string]int{"watch_high": 1, "watch_med": 1, "watch_low": 2},
		},
		{
			name: "rewatched episodes of one series are one rewatch",
			rewatches: []RewatchCount{
				{MediaItemID: binged[0], Count: 2},
				{MediaItemID: binged[1], Count: 3},
				{MediaItemID: binged[2], Count: 1},
				{MediaItemID: "movie-done", Count: 2},
			},
			want: map[string]int{"rewatch": 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := map[string]int{}
			countWatchSignals(counts, tc.progress, tc.rewatches, refs, now)
			if !maps.Equal(counts, tc.want) {
				t.Fatalf("counts = %v, want %v", counts, tc.want)
			}
		})
	}
}

// The taste vector is built from positive titles only: a disliked or
// abandoned title is left out rather than subtracted, while it still counts
// as a signal. Rated, favorited and watched titles count toward the
// cold-start level once each, however many signals they carry; a title only
// on the watchlist shapes the vector but does not count.
func TestPositiveTasteItemsKeepsPositiveTitlesOnly(t *testing.T) {
	stars := func(n int) *int { return &n }
	axis := func(i int) []float32 {
		v := make([]float32, 8)
		v[i] = 1
		return v
	}
	signals := map[string]*canonicalWeightComponents{
		"a-liked":         {ImplicitWeight: WeightWatchHigh},
		"b-loved":         {Rating: stars(5), ExplicitWeight: WeightRated5, ImplicitWeight: WeightWatchHigh, IntentWeight: WeightFavorited, Favorited: true},
		"c-seed-pick":     {IntentWeight: WeightFavorited, Favorited: true},
		"d-watchlisted":   {IntentWeight: WeightWatchlist},
		"e-disliked":      {Rating: stars(1), ExplicitWeight: WeightRatedLow, ImplicitWeight: WeightWatchHigh},
		"f-abandoned":     {ImplicitWeight: WeightWatchLow},
		"g-meh-unwatched": {Rating: stars(3)},
		"h-no-embedding":  {ImplicitWeight: WeightWatchHigh},
	}
	embeddings := map[string][]float32{}
	for i, id := range []string{"a-liked", "b-loved", "c-seed-pick", "d-watchlisted", "e-disliked", "f-abandoned", "g-meh-unwatched"} {
		embeddings[id] = axis(i)
	}

	items, levelTitles := positiveTasteItems(signals, embeddings)
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.itemID
	}
	if want := []string{"a-liked", "b-loved", "c-seed-pick", "d-watchlisted"}; !slices.Equal(ids, want) {
		t.Fatalf("taste items = %v, want %v", ids, want)
	}
	if levelTitles != 3 {
		t.Fatalf("titles counting toward the level = %d, want 3 (the watchlist-only title does not)", levelTitles)
	}
	vec := tasteVector(items)
	if vec[4] != 0 || vec[5] != 0 || vec[6] != 0 {
		t.Fatalf("taste vector = %v, want nothing along the disliked, abandoned and 3-star titles", vec)
	}
	if !(vec[1] > vec[0] && vec[0] > vec[3] && vec[3] > 0) {
		t.Fatalf("taste vector = %v, want loved > liked > watchlisted > 0", vec)
	}

	// With only dislikes left there is nothing to build a vector from.
	items, levelTitles = positiveTasteItems(map[string]*canonicalWeightComponents{
		"e-disliked":  signals["e-disliked"],
		"f-abandoned": signals["f-abandoned"],
	}, embeddings)
	if len(items) != 0 || levelTitles != 0 || tasteVector(items) != nil {
		t.Fatalf("negative-only profile: items = %v, level titles = %d; want none and no vector", items, levelTitles)
	}
}

// The taste-profile summary reports signals of each kind; the count of
// positive titles behind the cold-start level is not one.
func TestSummarySignalCountsLeavesOutPositiveTitles(t *testing.T) {
	got := summarySignalCounts(map[string]int{"favorited": 3, "watch_high": 2, signalCountPositiveTitles: 4})
	if want := map[string]int{"favorited": 3, "watch_high": 2}; !maps.Equal(got, want) {
		t.Fatalf("summary counts = %v, want %v", got, want)
	}
	if got := summarySignalCounts(nil); got == nil || len(got) != 0 {
		t.Fatalf("summary counts of no row = %v, want an empty map", got)
	}
}

// axisVector is a full-width vector pointing along axis, nudged along the
// axes in nudges, so test items sit next to each other and away from the
// rest of a shared test database.
func axisVector(axis int, nudges map[int]float32) []float32 {
	v := make([]float32, CanonicalEmbeddingDimensions)
	v[axis] = 1
	for i, n := range nudges {
		v[i] += n
	}
	return v
}

// newTasteTestAccount inserts an account and one of its profiles, removed
// with the account when the test ends.
func newTasteTestAccount(t *testing.T, pool *pgxpool.Pool, name string) (int, string) {
	t.Helper()
	ctx := context.Background()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES($1, 'user') RETURNING id`, name+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES($1, $2, $3)`, profileID, userID, name); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	return userID, profileID
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// ClearTasteProfile keeps the profile's row, with its signal counts and no
// vector, and drops its clusters and cached rows, leaving global rows and
// other profiles alone. The row can still be marked stale, and similar-user
// search neither finds the cleared profile nor runs for it.
func TestClearTasteProfileKeepsTheRowAndDropsPersonalStatePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	userID, cleared := newTasteTestAccount(t, pool, "taste-clear-")
	const kept = "kept"
	peerID, peer := newTasteTestAccount(t, pool, "taste-clear-peer-")

	started, err := repo.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vec := axisVector(1500, nil)
	expires := time.Now().Add(time.Hour).Format(time.RFC3339)
	items := []ScoredItem{{MediaItemID: "item", Score: 1}}
	personalRows := []struct{ recType, source string }{
		{RecTypeForYouMain, ""},
		{RecTypeForYouClusterPrefix + "0", ""},
		{RecTypeSimilarUsersLiked, ""},
		{RecTypeBecauseWatched, "anchor"},
	}
	for _, profile := range []string{cleared, kept} {
		if err := repo.UpsertTasteProfile(ctx, userID, profile, vec, map[string]int{"watch_high": 3}, "", started.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertTasteClusters(ctx, userID, profile, []TasteCluster{{ClusterIdx: 0, Embedding: vec, Label: "Drama", MemberCount: 1, TotalWeight: 1}}); err != nil {
			t.Fatal(err)
		}
		for _, row := range personalRows {
			if err := repo.UpsertRecommendationCache(ctx, userID, profile, row.recType, row.source, items, expires); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := repo.UpsertTasteProfile(ctx, peerID, peer, vec, map[string]int{"watch_high": 3}, "", started); err != nil {
		t.Fatal(err)
	}
	globalType := "test_clear_global_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM recommendation_cache WHERE user_id IS NULL AND rec_type = $1`, globalType)
	})
	if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, globalType, "", items, expires); err != nil {
		t.Fatal(err)
	}

	if err := repo.ClearTasteProfile(ctx, userID, cleared, map[string]int{"watch_low": 1}, "PG-13", started, true); err != nil {
		t.Fatalf("clear taste profile: %v", err)
	}

	if emb, err := repo.GetTasteProfile(ctx, userID, cleared); err != nil || emb != nil {
		t.Fatalf("cleared taste vector = %d dims, %v; want none", len(emb), err)
	}
	var vectorIsNull bool
	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL, updated_at FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`, userID, cleared).Scan(&vectorIsNull, &updatedAt); err != nil {
		t.Fatalf("read cleared row: %v", err)
	}
	if !vectorIsNull || !updatedAt.Equal(started) {
		t.Fatalf("cleared row: vector null = %v, updated_at = %v; want null and %v", vectorIsNull, updatedAt, started)
	}
	meta, err := repo.GetTasteProfileMeta(ctx, userID, cleared)
	if err != nil || meta == nil || !maps.Equal(meta.SignalCounts, map[string]int{"watch_low": 1}) || meta.MaxContentRating != "PG-13" {
		t.Fatalf("cleared meta = %+v, %v", meta, err)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`, userID, cleared); n != 0 {
		t.Fatalf("cleared profile keeps %d clusters", n)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id = $1 AND profile_id = $2`, userID, cleared); n != 0 {
		t.Fatalf("cleared profile keeps %d cached rows", n)
	}

	// Other profiles and the global rows are untouched.
	if emb, err := repo.GetTasteProfile(ctx, userID, kept); err != nil || emb == nil {
		t.Fatalf("other profile's vector = %v, %v", emb, err)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`, userID, kept); n != 1 {
		t.Fatalf("other profile has %d clusters, want 1", n)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id = $1 AND profile_id = $2`, userID, kept); n != len(personalRows) {
		t.Fatalf("other profile has %d cached rows, want %d", n, len(personalRows))
	}
	if got, err := repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, globalType, ""); err != nil || len(got) != 1 {
		t.Fatalf("global row = %v, %v", got, err)
	}

	// The kept row still takes stale marks.
	if err := repo.MarkProfileStale(ctx, userID, cleared); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2 AND stale_at > updated_at`, userID, cleared); n != 1 {
		t.Fatal("cleared profile could not be marked stale")
	}

	// Similar-user search skips the cleared profile and does not run for it.
	similar, err := repo.FindSimilarUsers(ctx, peerID, peer, "", 50)
	if err != nil {
		t.Fatalf("similar users next to a cleared profile: %v", err)
	}
	var foundKept bool
	for _, u := range similar {
		if u.UserID == userID && u.ProfileID == cleared {
			t.Fatal("similar-user search returned the cleared profile")
		}
		foundKept = foundKept || (u.UserID == userID && u.ProfileID == kept)
	}
	if !foundKept {
		t.Fatalf("similar users = %+v, want the kept profile", similar)
	}
	if similar, err := repo.FindSimilarUsers(ctx, userID, cleared, "", 50); err != nil || similar != nil {
		t.Fatalf("similar users of the cleared profile = %v, %v; want none", similar, err)
	}

	// A profile with no row yet gets one, so later changes can mark it.
	const fresh = "fresh"
	if err := repo.ClearTasteProfile(ctx, userID, fresh, map[string]int{}, "", started, true); err != nil {
		t.Fatal(err)
	}
	if meta, err := repo.GetTasteProfileMeta(ctx, userID, fresh); err != nil || meta == nil || len(meta.SignalCounts) != 0 {
		t.Fatalf("fresh profile meta = %+v, %v; want an empty row", meta, err)
	}

	// Without create an existing row is still cleared, but a missing one is
	// not created, while the profile's clusters and cached rows still go.
	if err := repo.ClearTasteProfile(ctx, userID, fresh, map[string]int{"rated_low": 2}, "", started, false); err != nil {
		t.Fatal(err)
	}
	if meta, err := repo.GetTasteProfileMeta(ctx, userID, fresh); err != nil || meta == nil || meta.SignalCounts["rated_low"] != 2 {
		t.Fatalf("fresh profile meta = %+v, %v; want the existing row updated", meta, err)
	}
	const gone = "gone"
	if err := repo.UpsertTasteClusters(ctx, userID, gone, []TasteCluster{{ClusterIdx: 0, Embedding: vec, Label: "Drama", MemberCount: 1, TotalWeight: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertRecommendationCache(ctx, userID, gone, RecTypeForYouMain, "", items, expires); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearTasteProfile(ctx, userID, gone, map[string]int{"rated_low": 1}, "", started, false); err != nil {
		t.Fatal(err)
	}
	if meta, err := repo.GetTasteProfileMeta(ctx, userID, gone); err != nil || meta != nil {
		t.Fatalf("meta without create = %+v, %v; want no row", meta, err)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`, userID, gone); n != 0 {
		t.Fatalf("profile cleared without create keeps %d clusters", n)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id = $1 AND profile_id = $2`, userID, gone); n != 0 {
		t.Fatalf("profile cleared without create keeps %d cached rows", n)
	}
}

// A refresh that runs after its profile was deleted, as a queued one can,
// writes no taste row: not from no signal, and not from a rewatch in the
// watch history the deletion keeps. A row left behind loses its vector and
// clusters. A live profile with no signal still gets its row.
func TestRefreshWritesNoTasteRowForADeletedProfilePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttaste-deleted-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, live := newTasteTestAccount(t, pool, prefix)
	movie := prefix + "movie"
	seedRecoMediaItem(t, pool, movie, "movie", "matched")
	repo := NewRepo(pool)
	vec := axisVector(1750, nil)
	if err := repo.UpsertEmbedding(ctx, movie, vec, "test-model", movie); err != nil {
		t.Fatal(err)
	}
	provider := pgstore.NewPostgresProvider(pool)
	engine := NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, provider, config.RecommendationsConfig{})

	gone := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES($1, $2, 'gone')`, gone, userID); err != nil {
		t.Fatal(err)
	}
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProfile(ctx, gone); err != nil {
		t.Fatal(err)
	}

	refresh := func(profile string) {
		t.Helper()
		if stored, err := engine.refreshTasteProfile(ctx, userID, profile); err != nil || stored {
			t.Fatalf("refresh of %s stored a vector = %v, %v; want none", profile, stored, err)
		}
	}
	tasteRows := func(profile string) int {
		t.Helper()
		return countRows(t, pool, `SELECT COUNT(*) FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`, userID, profile)
	}

	refresh(gone)
	if n := tasteRows(gone); n != 0 {
		t.Fatalf("deleted profile with no signal has %d taste rows, want none", n)
	}

	for range 2 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_history(id, user_id, profile_id, media_item_id, duration_seconds, completed)
			VALUES(gen_random_uuid()::text, $1, $2, $3, 5400, true)`, userID, gone, movie); err != nil {
			t.Fatal(err)
		}
	}
	refresh(gone)
	if n := tasteRows(gone); n != 0 {
		t.Fatalf("deleted profile with a rewatch has %d taste rows, want none", n)
	}

	started, err := repo.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTasteProfile(ctx, userID, gone, vec, map[string]int{"rewatch": 1}, "", started); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTasteClusters(ctx, userID, gone, []TasteCluster{{ClusterIdx: 0, Embedding: vec, Label: "Drama", MemberCount: 1, TotalWeight: 1}}); err != nil {
		t.Fatal(err)
	}
	refresh(gone)
	if emb, err := repo.GetTasteProfile(ctx, userID, gone); err != nil || emb != nil {
		t.Fatalf("leftover taste vector = %d dims, %v; want it cleared", len(emb), err)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`, userID, gone); n != 0 {
		t.Fatalf("leftover profile keeps %d clusters", n)
	}

	refresh(live)
	if meta, err := repo.GetTasteProfileMeta(ctx, userID, live); err != nil || meta == nil || len(meta.SignalCounts) != 1 || meta.SignalCounts[signalCountPositiveTitles] != 0 {
		t.Fatalf("live profile meta = %+v, %v; want a row with no positive titles", meta, err)
	}
}

// A refresh that finds no positive signal clears the taste vector an earlier
// refresh stored, with its clusters and cached rows, and records the signal
// counts it did find.
func TestRefreshClearsATasteProfileWithNoPositiveSignalPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttaste-negative-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, profile := newTasteTestAccount(t, pool, prefix)
	movie := prefix + "movie"
	seedRecoMediaItem(t, pool, movie, "movie", "matched")
	repo := NewRepo(pool)
	if err := repo.UpsertEmbedding(ctx, movie, axisVector(1700, nil), "test-model", movie); err != nil {
		t.Fatal(err)
	}
	engine := newTasteTestEngine(pool)
	rate := func(stars int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, $4)
			ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET rating = EXCLUDED.rating, rated_at = NOW()`,
			userID, profile, movie, stars); err != nil {
			t.Fatal(err)
		}
	}
	refresh := func(wantVector bool, wantCounts map[string]int) {
		t.Helper()
		stored, err := engine.refreshTasteProfile(ctx, userID, profile)
		if err != nil || stored != wantVector {
			t.Fatalf("refresh stored a vector = %v, %v; want %v", stored, err, wantVector)
		}
		emb, err := repo.GetTasteProfile(ctx, userID, profile)
		if err != nil || (emb != nil) != wantVector {
			t.Fatalf("taste vector = %d dims, %v; want present = %v", len(emb), err, wantVector)
		}
		meta, err := repo.GetTasteProfileMeta(ctx, userID, profile)
		if err != nil || meta == nil || !maps.Equal(meta.SignalCounts, wantCounts) {
			t.Fatalf("meta = %+v, %v; want counts %v", meta, err, wantCounts)
		}
	}

	rate(5)
	refresh(true, map[string]int{"rated_5": 1, signalCountPositiveTitles: 1})
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`, userID, profile); n != 1 {
		t.Fatalf("clusters after a positive refresh = %d, want 1", n)
	}
	if err := repo.UpsertRecommendationCache(ctx, userID, profile, RecTypeForYouMain, "", []ScoredItem{{MediaItemID: "x", Score: 1}}, time.Now().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	rate(1)
	refresh(false, map[string]int{"rated_low": 1, signalCountPositiveTitles: 0})
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_clusters WHERE user_id = $1 AND profile_id = $2`, userID, profile); n != 0 {
		t.Fatalf("clusters after a negative-only refresh = %d, want none", n)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id = $1 AND profile_id = $2`, userID, profile); n != 0 {
		t.Fatalf("cached rows after a negative-only refresh = %d, want none", n)
	}

	// Removing the last signal also leaves a cleared row behind.
	if _, err := pool.Exec(ctx, `DELETE FROM user_ratings WHERE user_id = $1 AND profile_id = $2`, userID, profile); err != nil {
		t.Fatal(err)
	}
	refresh(false, map[string]int{signalCountPositiveTitles: 0})
}

// newTasteTestEngine is an engine over pool with the Postgres user store.
func newTasteTestEngine(pool *pgxpool.Pool) *Engine {
	return NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, pgstore.NewPostgresProvider(pool), config.RecommendationsConfig{})
}

// A refresh records the profile's positive titles, each once however many
// signals it has, and the cold-start level follows them: three taste-seed
// picks are level 2, a single title rated, favorited and finished or a
// single long series is level 1, and a profile of watchlist adds, or of
// titles with no embedding yet, is level 0. The taste-profile summary leaves
// the count out.
func TestRefreshCountsPositiveTitlesForTheLevelPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttaste-level-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	engine := newTasteTestEngine(pool)

	movies := make([]string, 6)
	for i := range movies {
		movies[i] = fmt.Sprintf("%smovie-%d", prefix, i)
		seedRecoMediaItem(t, pool, movies[i], "movie", "matched")
		if err := repo.UpsertEmbedding(ctx, movies[i], axisVector(1800+i, nil), "test-model", movies[i]); err != nil {
			t.Fatal(err)
		}
	}
	unembedded := prefix + "movie-unembedded"
	seedRecoMediaItem(t, pool, unembedded, "movie", "matched")
	series := prefix + "series"
	seedRecoMediaItem(t, pool, series, "series", "matched")
	if err := repo.UpsertEmbedding(ctx, series, axisVector(1810, nil), "test-model", series); err != nil {
		t.Fatal(err)
	}
	episodes := make([]string, 15)
	for i := range episodes {
		episodes[i] = fmt.Sprintf("%sepisode-%02d", prefix, i)
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id, series_id, season_number, episode_number, title) VALUES($1, $2, 1, $3, 'Episode')`, episodes[i], series, i+1); err != nil {
			t.Fatal(err)
		}
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	favorite := func(userID int, profile string, ids ...string) {
		for _, id := range ids {
			exec(`INSERT INTO user_favorites(user_id, profile_id, media_item_id) VALUES($1, $2, $3)`, userID, profile, id)
		}
	}
	watchlist := func(userID int, profile string, ids ...string) {
		for _, id := range ids {
			exec(`INSERT INTO user_watchlist(user_id, profile_id, media_item_id) VALUES($1, $2, $3)`, userID, profile, id)
		}
	}
	finish := func(userID int, profile string, ids ...string) {
		for _, id := range ids {
			exec(`INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, updated_at) VALUES($1, $2, $3, true, NOW())`, userID, profile, id)
		}
	}

	for _, tc := range []struct {
		name       string
		seed       func(userID int, profile string)
		wantVector bool
		wantCounts map[string]int
		wantLevel  int
	}{
		{
			name:       "three taste-seed picks",
			seed:       func(u int, p string) { favorite(u, p, movies[0], movies[1], movies[2]) },
			wantVector: true,
			wantCounts: map[string]int{"favorited": 3, signalCountPositiveTitles: 3},
			wantLevel:  2,
		},
		{
			name: "one title rated, favorited and finished",
			seed: func(u int, p string) {
				exec(`INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 5)`, u, p, movies[3])
				favorite(u, p, movies[3])
				finish(u, p, movies[3])
			},
			wantVector: true,
			wantCounts: map[string]int{"rated_5": 1, "favorited": 1, "watch_high": 1, signalCountPositiveTitles: 1},
			wantLevel:  1,
		},
		{
			name: "one long series with two favorited episodes",
			seed: func(u int, p string) {
				finish(u, p, episodes...)
				favorite(u, p, episodes[0], episodes[1])
			},
			wantVector: true,
			wantCounts: map[string]int{"watch_high": 1, "favorited": 1, signalCountPositiveTitles: 1},
			wantLevel:  1,
		},
		{
			name:       "watchlist only",
			seed:       func(u int, p string) { watchlist(u, p, movies[3], movies[4], movies[5]) },
			wantVector: true,
			wantCounts: map[string]int{"watchlist": 3, signalCountPositiveTitles: 0},
			wantLevel:  0,
		},
		{
			name:       "a pick with no embedding yet",
			seed:       func(u int, p string) { favorite(u, p, unembedded) },
			wantVector: false,
			wantCounts: map[string]int{"favorited": 1, signalCountPositiveTitles: 0},
			wantLevel:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID, profile := newTasteTestAccount(t, pool, prefix)
			tc.seed(userID, profile)
			stored, err := engine.refreshTasteProfile(ctx, userID, profile)
			if err != nil || stored != tc.wantVector {
				t.Fatalf("refresh stored a vector = %v, %v; want %v", stored, err, tc.wantVector)
			}
			meta, err := repo.GetTasteProfileMeta(ctx, userID, profile)
			if err != nil || meta == nil || !maps.Equal(meta.SignalCounts, tc.wantCounts) {
				t.Fatalf("meta = %+v, %v; want counts %v", meta, err, tc.wantCounts)
			}
			if got := coldStartLevelOf(meta); got != tc.wantLevel {
				t.Fatalf("level = %d, want %d", got, tc.wantLevel)
			}
			summary, err := engine.GetTasteProfileSummary(ctx, userID, profile)
			if err != nil {
				t.Fatal(err)
			}
			wantSummary := maps.Clone(tc.wantCounts)
			delete(wantSummary, signalCountPositiveTitles)
			if !maps.Equal(summary.SignalCounts, wantSummary) {
				t.Fatalf("summary counts = %v, want %v", summary.SignalCounts, wantSummary)
			}
		})
	}
}

// A 3-star completion pulls the taste vector less than an unrated
// completion, where it used to pull more.
func TestRefreshWeighsAThreeStarCompletionBelowAnUnratedOnePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttaste-meh-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	userID, profile := newTasteTestAccount(t, pool, prefix)
	meh, plain := prefix+"meh", prefix+"plain"
	for i, id := range []string{meh, plain} {
		seedRecoMediaItem(t, pool, id, "movie", "matched")
		if err := repo.UpsertEmbedding(ctx, id, axisVector(1820+i, nil), "test-model", id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, updated_at) VALUES($1, $2, $3, true, NOW())`, userID, profile, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 3)`, userID, profile, meh); err != nil {
		t.Fatal(err)
	}

	if stored, err := newTasteTestEngine(pool).refreshTasteProfile(ctx, userID, profile); err != nil || !stored {
		t.Fatalf("refresh stored a vector = %v, %v", stored, err)
	}
	vec, err := repo.GetTasteProfile(ctx, userID, profile)
	if err != nil || vec == nil {
		t.Fatalf("taste vector = %v, %v", vec, err)
	}
	if mehPull, plainPull := vec[1820], vec[1821]; !(mehPull > 0 && mehPull < plainPull) {
		t.Fatalf("pull of the 3-star completion = %v, of the unrated one = %v; want 0 < 3-star < unrated", mehPull, plainPull)
	}
	meta, err := repo.GetTasteProfileMeta(ctx, userID, profile)
	if err != nil || meta == nil || meta.SignalCounts["rated_3"] != 1 || meta.SignalCounts[signalCountPositiveTitles] != 2 {
		t.Fatalf("meta = %+v, %v; want rated_3 counted and both titles positive", meta, err)
	}
}

// The taste card's genres are the clusters' genres, taken in turns from the
// heaviest cluster down, so a second interest shows even when the heaviest
// cluster has genres to fill the card.
func TestClusterTopGenresTakeTurnsAcrossClusters(t *testing.T) {
	horror := TasteCluster{ClusterIdx: 0, DominantGenres: []string{"Horror", "Thriller", "Mystery"}, TotalWeight: 12}
	romance := TasteCluster{ClusterIdx: 1, DominantGenres: []string{"Romance", "Comedy", "Drama"}, TotalWeight: 13}
	horrorComedy := TasteCluster{ClusterIdx: 2, DominantGenres: []string{"Horror", "Comedy"}, TotalWeight: 5}
	for _, tc := range []struct {
		name     string
		clusters []TasteCluster
		want     []string
	}{
		{"two interests", []TasteCluster{horror, romance}, []string{"Romance", "Horror", "Comedy", "Thriller", "Drama"}},
		{"a repeated genre counts once", []TasteCluster{horror, romance, horrorComedy}, []string{"Romance", "Horror", "Comedy", "Thriller", "Drama"}},
		{"one cluster", []TasteCluster{horrorComedy}, []string{"Horror", "Comedy"}},
		{"no clusters", nil, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterTopGenres(tc.clusters, summaryTopGenres); !slices.Equal(got, tc.want) {
				t.Fatalf("genres = %v, want %v", got, tc.want)
			}
		})
	}
}

// The taste-profile summary takes its genres from the profile's clusters,
// so a profile that never rated or favorited a title still has them.
func TestTasteProfileSummaryGenresComeFromTheClustersPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	userID, profile := newTasteTestAccount(t, pool, "ttaste-card-")
	taste := axisVector(1830, nil)
	if err := repo.UpsertTasteProfile(ctx, userID, profile, taste, map[string]int{"watch_high": 25, signalCountPositiveTitles: 25}, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTasteClusters(ctx, userID, profile, []TasteCluster{
		{ClusterIdx: 0, Embedding: taste, DominantGenres: []string{"Horror", "Thriller"}, Label: "Horror", MemberCount: 12, TotalWeight: 9},
		{ClusterIdx: 1, Embedding: taste, DominantGenres: []string{"Romance", "Comedy"}, Label: "Romance", MemberCount: 13, TotalWeight: 10},
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := newTasteTestEngine(pool).GetTasteProfileSummary(ctx, userID, profile)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Romance", "Horror", "Comedy", "Thriller"}; !slices.Equal(summary.TopGenres, want) {
		t.Fatalf("top genres = %v, want %v", summary.TopGenres, want)
	}
}
