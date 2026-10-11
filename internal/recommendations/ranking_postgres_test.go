package recommendations

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// rankingLibrary inserts a library of kind, removed when the test ends.
func rankingLibrary(t *testing.T, pool *pgxpool.Pool, name, kind string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, kind, name).Scan(&id); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id) })
	return id
}

// seedRankedItem inserts a title in library with a content rating, and an
// embedding when vec is not nil.
func seedRankedItem(t *testing.T, pool *pgxpool.Pool, id, mediaType, status string, library int, rating string, age int, vec []float32) {
	t.Helper()
	ctx := t.Context()
	seedRecoMediaItem(t, pool, id, mediaType, status)
	if _, err := pool.Exec(ctx, `UPDATE media_items SET content_rating = $2, content_rating_age = $3 WHERE content_id = $1`, id, rating, age); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library); err != nil {
		t.Fatal(err)
	}
	if vec != nil {
		if err := NewRepo(pool).UpsertEmbedding(ctx, id, vec, "test-model", id); err != nil {
			t.Fatal(err)
		}
	}
}

// A media type counts as present only with a matched, embedded title the
// viewer's libraries and maturity limits admit.
func TestPresentMediaTypesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tpresent-types-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	movies := rankingLibrary(t, pool, prefix+"movies", "movies")
	shows := rankingLibrary(t, pool, prefix+"shows", "series")
	vec := axisVector(2600, nil)
	seedRankedItem(t, pool, prefix+"movie", "movie", "matched", movies, "G", 0, vec)
	seedRankedItem(t, pool, prefix+"series-without-embedding", "series", "matched", shows, "G", 0, nil)
	seedRankedItem(t, pool, prefix+"series-unmatched", "series", "unmatched", shows, "G", 0, vec)

	types := []string{"movie", "series", "audiobook"}
	check := func(name string, filter catalog.AccessFilter, want ...string) {
		t.Helper()
		got, err := repo.PresentMediaTypes(ctx, types, filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, want) {
			t.Fatalf("%s: present = %v, want %v", name, keys, want)
		}
	}
	ours := catalog.AccessFilter{AllowedLibraryIDs: []int{movies, shows}}
	check("no usable series", ours, "movie")

	seedRankedItem(t, pool, prefix+"series-adult", "series", "matched", shows, "R", 17, vec)
	check("an embedded matched series", ours, "movie", "series")
	check("series above the rating ceiling", catalog.AccessFilter{AllowedLibraryIDs: ours.AllowedLibraryIDs, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}, "movie")
	check("TV library only", catalog.AccessFilter{AllowedLibraryIDs: []int{shows}}, "series")
	check("TV library hidden", catalog.AccessFilter{AllowedLibraryIDs: ours.AllowedLibraryIDs, DisabledLibraryIDs: []int{shows}}, "movie")
	check("no library allowed", catalog.AccessFilter{AllowedLibraryIDs: []int{}})
}

// The quality rating is IMDb's, else TMDB's below 9.5; a title with neither
// has none.
func TestGetItemQualityRatingsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tquality-ratings-"
	cleanupRecoMediaItems(t, pool, prefix)
	for id, ratings := range map[string][2]any{
		"both":      {7.1, 8.0},
		"tmdb-only": {nil, 6.4},
		"tmdb-high": {nil, 9.8},
		"none":      {nil, nil},
	} {
		seedRecoMediaItem(t, pool, prefix+id, "movie", "matched")
		if _, err := pool.Exec(ctx, `UPDATE media_items SET rating_imdb = $2, rating_tmdb = $3 WHERE content_id = $1`, prefix+id, ratings[0], ratings[1]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := NewRepo(pool).GetItemQualityRatings(ctx, []string{prefix + "both", prefix + "tmdb-only", prefix + "tmdb-high", prefix + "none", prefix + "missing"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{prefix + "both": 7.1, prefix + "tmdb-only": 6.4}
	if !maps.Equal(got, want) {
		t.Fatalf("ratings = %v, want %v", got, want)
	}
}

// A movie lover's main row is all movies near its taste, yet the TV
// library's "Recommended for You", which reads the whole cached row and keeps
// that library's titles, still gets at least mediaTypeFloor series: the cache
// build places the best series after the served window.
func TestTVLibrarySectionFillsFromTheMainRowTailPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttv-section-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, profile := newTasteTestAccount(t, pool, prefix)
	movies := rankingLibrary(t, pool, prefix+"movies", "movies")
	shows := rankingLibrary(t, pool, prefix+"shows", "series")

	// More movies near the taste than the main row's candidate query reads,
	// so its candidates hold no series; the series sit further away.
	const axis = 2700
	for i := range CacheCandidateLimit*3 + 10 {
		seedRankedItem(t, pool, fmt.Sprintf("%smovie-%03d", prefix, i), "movie", "matched", movies, "G", 0,
			axisVector(axis, map[int]float32{axis + 1 + i: 0.15}))
	}
	for i := range 20 {
		seedRankedItem(t, pool, fmt.Sprintf("%sseries-%02d", prefix, i), "series", "matched", shows, "G", 0,
			axisVector(axis, map[int]float32{2950: 1.5, 2951 + i: 0.1}))
	}
	repo := NewRepo(pool)
	taste := axisVector(axis, nil)
	now := time.Now()
	if err := repo.UpsertTasteProfile(ctx, userID, profile, taste, map[string]int{"watch_high": 30}, "", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertTasteClusters(ctx, userID, profile, []TasteCluster{{ClusterIdx: 0, Embedding: taste, Label: "Test", MemberCount: 30, TotalWeight: 20}}); err != nil {
		t.Fatal(err)
	}

	provider := pgstore.NewPostgresProvider(pool)
	engine := NewEngine(pool, catalog.NewRatingsRepo(pool), catalog.NewItemRepository(pool), nil, provider, config.RecommendationsConfig{DiversityLambda: 0.7})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	if built := w.cacheUserRows(ctx, engine.repo, userID, profile, cacheExpiry(now)); built.failed != 0 {
		t.Fatalf("cache build = %+v, want no failures", built)
	}

	main, err := repo.GetRecommendationCache(ctx, userID, profile, RecTypeForYouMain, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(main) != CacheCandidateLimit {
		t.Fatalf("main row has %d items, want %d", len(main), CacheCandidateLimit)
	}
	for _, item := range main[:ServedRowSize] {
		if !strings.HasPrefix(item.MediaItemID, prefix+"movie-") {
			t.Fatalf("served window holds %s, want only the nearest movies", item.MediaItemID)
		}
	}

	reader := NewReader(repo, catalog.NewRatingsRepo(pool), nil, provider)
	row, err := reader.SectionForYouMain(ctx, userID, profile, catalog.AccessFilter{UserID: userID, ProfileID: profile, AllowedLibraryIDs: []int{shows}})
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("TV library row is empty")
	}
	if series := prefixed(row.Items, prefix+"series-"); len(series) < mediaTypeFloor(CacheCandidateLimit) {
		t.Fatalf("TV library row holds %d series %v, want at least %d", len(series), series, mediaTypeFloor(CacheCandidateLimit))
	}
}
