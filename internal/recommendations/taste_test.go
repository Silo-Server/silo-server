package recommendations

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// axisVector is a full-width vector pointing along axis, nudged along the
// axes in nudges, so test items sit next to each other and away from the
// rest of a shared test database.
//
// Every component also gets random noise far below the nudges. Without it,
// each vector sits at exactly the same distance from every other test's
// vectors, and sibling items at exactly the same distance from each other.
// The HNSW index breaks those ties against new elements, so once neighbor
// lists are full (after a vacuum refills them, or when a test seeds dozens
// of siblings) items can be inserted with no incoming edges, and the index
// scan never reaches them.
func axisVector(axis int, nudges map[int]float32) []float32 {
	v := make([]float32, CanonicalEmbeddingDimensions)
	for i := range v {
		v[i] = rand.Float32() * 1e-4
	}
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

// deletingStoreProvider deletes a profile, and purges its recommendation
// state as the profile handler does, right after the first time a refresh
// checks that the profile exists: the deletion lands between that check and
// the refresh's writes.
type deletingStoreProvider struct {
	userstore.UserStoreProvider
	repo    *Repo
	userID  int
	profile string
	checks  int
}

func (p *deletingStoreProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	store, err := p.UserStoreProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return deletingStore{UserStore: store, p: p}, nil
}

type deletingStore struct {
	userstore.UserStore
	p *deletingStoreProvider
}

func (s deletingStore) GetProfile(ctx context.Context, profileID string) (*userstore.Profile, error) {
	profile, err := s.UserStore.GetProfile(ctx, profileID)
	s.p.checks++
	if err != nil || s.p.checks > 1 || profileID != s.p.profile {
		return profile, err
	}
	if _, err := s.DeleteProfile(ctx, profileID); err != nil {
		return nil, err
	}
	return profile, s.p.repo.PurgeProfile(ctx, s.p.userID, profileID)
}

// A profile deleted after a refresh checked that it exists, and purged before
// the refresh wrote, does not keep the taste row the refresh then writes: the
// refresh checks again after its last write and purges what it wrote.
func TestRefreshPurgesAProfileDeletedWhileItWritesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttaste-racing-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, _ := newTasteTestAccount(t, pool, prefix)
	movie := prefix + "movie"
	seedRecoMediaItem(t, pool, movie, "movie", "matched")
	repo := NewRepo(pool)
	if err := repo.UpsertEmbedding(ctx, movie, axisVector(1750, nil), "test-model", movie); err != nil {
		t.Fatal(err)
	}
	racing := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES($1, $2, 'racing')`, racing, userID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_history(id, user_id, profile_id, media_item_id, duration_seconds, completed)
			VALUES(gen_random_uuid()::text, $1, $2, $3, 5400, true)`, userID, racing, movie); err != nil {
			t.Fatal(err)
		}
	}
	provider := &deletingStoreProvider{UserStoreProvider: pgstore.NewPostgresProvider(pool), repo: repo, userID: userID, profile: racing}
	engine := NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, provider, config.RecommendationsConfig{})

	stored, err := engine.refreshTasteProfile(ctx, userID, racing)
	if err != nil || !stored {
		t.Fatalf("refresh = %v, %v; want a stored vector the recheck then purges", stored, err)
	}
	if provider.checks != 2 {
		t.Fatalf("existence checks = %d, want one before the writes and one after", provider.checks)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`, userID, racing); n != 0 {
		t.Fatalf("profile deleted during its refresh has %d taste rows, want none", n)
	}
}

// The cache job likewise purges the rows it wrote for a profile deleted
// after its build checked that the profile exists.
func TestCacheJobPurgesAProfileDeletedWhileItWritesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttaste-cache-racing-"
	userID, _ := newTasteTestAccount(t, pool, prefix)
	repo := NewRepo(pool)
	racing := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES($1, $2, 'racing')`, racing, userID); err != nil {
		t.Fatal(err)
	}
	provider := &deletingStoreProvider{UserStoreProvider: pgstore.NewPostgresProvider(pool), repo: repo, userID: userID, profile: racing}
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, provider, config.RecommendationsConfig{})

	built := w.cacheProfileRows(ctx, repo, userID, racing, cacheExpiry(time.Now()))
	if built.failed != 0 || built.cached == 0 {
		t.Fatalf("build = %+v; want rows written, then purged", built)
	}
	if provider.checks != 2 {
		t.Fatalf("existence checks = %d, want one before the writes and one after", provider.checks)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id = $1 AND profile_id = $2`, userID, racing); n != 0 {
		t.Fatalf("profile deleted during its cache build has %d cached rows, want none", n)
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
	if _, err := store.DeleteProfile(ctx, gone); err != nil {
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
			userID, profile, movie, catalog.ScoreFromStars(stars)); err != nil {
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
				exec(`INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 10)`, u, p, movies[3])
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
	if _, err := pool.Exec(ctx, `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 6)`, userID, profile, meh); err != nil {
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

// A long history builds the taste profile from a bounded set of titles:
// Not interested and non-positive titles never count, titles the profile
// rated, favorited or watched come before watchlist-only ones, and within each
// the heaviest come first.
func TestRankedPositiveTitlesPutsRealHistoryFirst(t *testing.T) {
	five := 5
	signals := map[string]*canonicalWeightComponents{
		"watched-light": {ImplicitWeight: 0.2},
		"rated":         {Rating: &five, ExplicitWeight: 2},
		"tie-b":         {ImplicitWeight: 1},
		"tie-a":         {ImplicitWeight: 1},
		"watchlist":     {IntentWeight: 9},
		"marked":        {ImplicitWeight: 9, NotInterested: true},
		"disliked":      {ImplicitWeight: -1},
	}
	want := []string{"rated", "tie-a", "tie-b", "watched-light", "watchlist"}
	if got := rankedPositiveTitles(signals); !slices.Equal(got, want) {
		t.Errorf("rankedPositiveTitles = %v, want %v", got, want)
	}
}

// The cap counts titles with an embedding: titles without one are passed
// over, and reading continues until the cap is filled or the titles run out.
func TestFirstEmbeddedTitlesSkipsTitlesWithoutAnEmbedding(t *testing.T) {
	stored := map[string][]float32{"b": {1}, "d": {1}, "e": {1}, "f": {1}}
	fetch := func(ids []string) (map[string][]float32, error) {
		out := map[string][]float32{}
		for _, id := range ids {
			if v, ok := stored[id]; ok {
				out[id] = v
			}
		}
		return out, nil
	}
	ranked := []string{"a", "b", "c", "d", "e", "f"}
	tests := []struct {
		limit int
		want  []string
	}{
		{limit: 2, want: []string{"b", "d"}},
		{limit: 3, want: []string{"b", "d", "e"}},
		{limit: 10, want: []string{"b", "d", "e", "f"}},
		{limit: 0, want: []string{}},
	}
	for _, tt := range tests {
		got, err := firstEmbeddedTitles(ranked, tt.limit, fetch)
		if err != nil {
			t.Fatal(err)
		}
		if ids := slices.Sorted(maps.Keys(got)); !slices.Equal(ids, tt.want) {
			t.Errorf("firstEmbeddedTitles(limit %d) = %v, want %v", tt.limit, ids, tt.want)
		}
	}
}
