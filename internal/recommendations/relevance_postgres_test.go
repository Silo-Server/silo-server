package recommendations

import (
	"encoding/json"
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

// Postgres checks of what reaches a profile, after the background refresh
// that builds its rows: nothing it watched, favorited or may not see, rows
// that differ between two profiles of one account, and rows for a profile
// on its first request. The metrics are defined in
// docs/architecture/recommendations-evaluation.md; the bounds here are loose
// regression guards, not the validators' thresholds.

// relevanceFixture holds the engine, worker and reader of one test, reading
// signals through the Postgres user store as production does.
type relevanceFixture struct {
	pool   *pgxpool.Pool
	repo   *Repo
	engine *Engine
	worker *Worker
	reader *Reader
	prefix string
}

func newRelevanceFixture(t *testing.T, name string) *relevanceFixture {
	t.Helper()
	pool := newEngineTestPool(t)
	prefix := "trel-" + name + "-" + uuid.NewString()[:8] + "-"
	cleanupRecoMediaItems(t, pool, prefix)
	store := pgstore.NewPostgresProvider(pool)
	ratings := catalog.NewRatingsRepo(pool)
	engine := NewEngine(pool, ratings, catalog.NewItemRepository(pool), nil, store, config.RecommendationsConfig{DiversityLambda: 0.7})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	w.staleMarker = engine.repo
	return &relevanceFixture{
		pool:   pool,
		repo:   engine.repo,
		engine: engine,
		worker: w,
		reader: NewReader(engine.repo, ratings, nil, store),
		prefix: prefix,
	}
}

// movie seeds an embedded, matched movie with the given genres and rating at
// vec, and returns its content ID.
func (f *relevanceFixture) movie(t *testing.T, name string, genres []string, rating string, ratingAge int, vec []float32) string {
	t.Helper()
	id := f.prefix + name
	seedRecoMediaItem(t, f.pool, id, "movie", "matched")
	if _, err := f.pool.Exec(t.Context(), `UPDATE media_items SET genres = $2, content_rating = $3, content_rating_age = $4 WHERE content_id = $1`, id, genres, rating, ratingAge); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.UpsertEmbedding(t.Context(), id, vec, "test-model", id); err != nil {
		t.Fatal(err)
	}
	return id
}

// profile adds a profile to the account, with a rating ceiling when
// maxRating is set.
func (f *relevanceFixture) profile(t *testing.T, userID int, name, maxRating string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO user_profiles(id, user_id, name, max_content_rating) VALUES($1, $2, $3, $4)`, id, userID, name, maxRating); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	return id
}

// finish records completed watches of items, the first most recently.
func (f *relevanceFixture) finish(t *testing.T, userID int, profileID string, items ...string) {
	t.Helper()
	for i, id := range items {
		if _, err := f.pool.Exec(t.Context(), `
			INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, updated_at)
			VALUES ($1, $2, $3, true, NOW() - $4 * INTERVAL '1 hour')`, userID, profileID, id, i+1); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *relevanceFixture) favorite(t *testing.T, userID int, profileID string, items ...string) {
	t.Helper()
	for _, id := range items {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO user_favorites(user_id, profile_id, media_item_id) VALUES($1, $2, $3)`, userID, profileID, id); err != nil {
			t.Fatal(err)
		}
	}
}

// cachedPersonalRows returns the item IDs of every row cached for the
// profile, keyed by row type and anchor.
func (f *relevanceFixture) cachedPersonalRows(t *testing.T, userID int, profileID string) map[string][]string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `
		SELECT rec_type, source_item_id, items FROM recommendation_cache
		WHERE  user_id = $1 AND profile_id = $2`, userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cached := map[string][]string{}
	for rows.Next() {
		var recType, source string
		var raw []byte
		if err := rows.Scan(&recType, &source, &raw); err != nil {
			t.Fatal(err)
		}
		var items []ScoredItem
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatal(err)
		}
		cached[recType+"|"+source] = scoredIDs(items)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cached
}

// servedRows reads what the profile is served: the For You page, Discover,
// Because You Watched and Similar Users, under its own access filter.
func (f *relevanceFixture) servedRows(t *testing.T, userID int, profileID string) map[string][]string {
	t.Helper()
	ctx := t.Context()
	filter, err := f.engine.profileAccessFilter(ctx, userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	served := map[string][]string{}
	add := func(source string, rows []ForYouRow, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		for i, row := range rows {
			served[fmt.Sprintf("%s %d %s %s", source, i, row.Type, row.Label)] = scoredIDs(row.Items)
		}
	}
	page, err := f.reader.GetForYouPage(ctx, userID, profileID, 50, filter)
	add("page", page, err)
	discover, err := f.reader.GetDiscoverRows(ctx, userID, profileID, 50, filter)
	add("discover", discover, err)
	watched, err := f.reader.GetBecauseYouWatchedRows(ctx, userID, profileID, 3, 50, filter)
	add("because-watched", watched, err)
	similar, err := f.reader.GetSimilarUsersLiked(ctx, userID, profileID, 50, filter)
	add("similar-users", []ForYouRow{{Type: "similar_users_liked", Items: similar}}, err)
	return served
}

// personalIDs is every item of the profile's personal rows on the page.
func personalIDs(rows []ForYouRow) []string {
	var ids []string
	for _, row := range rows {
		if row.Type == "cluster" || row.Type == "similar_users_liked" {
			ids = append(ids, scoredIDs(row.Items)...)
		}
	}
	return ids
}

// jaccard is the share of the distinct titles of a and b that both hold.
func jaccard(a, b []string) float64 {
	inA := map[string]bool{}
	for _, id := range a {
		inA[id] = true
	}
	union, shared := maps.Clone(inA), map[string]bool{}
	for _, id := range b {
		if inA[id] {
			shared[id] = true
		}
		union[id] = true
	}
	if len(union) == 0 {
		return 0
	}
	return float64(len(shared)) / float64(len(union))
}

// A full refresh caches no row holding a title the profile finished,
// favorited or may not see, and the rows it is served hold none either.
// The fixture puts R-rated titles nearest the taste of a PG profile and
// three peers behind an R title and a watched one, so a row built or read
// without the filters would carry them.
func TestRelevanceRubricLeakagePostgres(t *testing.T) {
	f := newRelevanceFixture(t, "leak")
	ctx := t.Context()
	const axis = 2700
	genres := [][]string{{"Animation"}, {"Family"}, {"Adventure"}}

	var kids, adult []string
	for i := range 24 {
		kids = append(kids, f.movie(t, fmt.Sprintf("kids-%02d", i), genres[i%3], "G", 0, axisVector(axis, map[int]float32{axis + 1 + i: 0.05})))
	}
	for i := range 12 {
		adult = append(adult, f.movie(t, fmt.Sprintf("adult-%02d", i), genres[i%3], "R", 17, axisVector(axis, map[int]float32{axis + 40 + i: 0.01})))
	}

	userID, _ := newTasteTestAccount(t, f.pool, f.prefix+"account-")
	kid := f.profile(t, userID, "Kid", "PG")
	finished, favorited := kids[:10], kids[10:12]
	f.finish(t, userID, kid, finished...)
	f.favorite(t, userID, kid, favorited...)

	// Three peer accounts share the taste and rate an R title, a title the
	// kid finished and an open one.
	for i := range minSimilarUsersPeerAccounts {
		peerID, peer := newTasteTestAccount(t, f.pool, fmt.Sprintf("%speer-%d-", f.prefix, i))
		if err := f.repo.UpsertTasteProfile(ctx, peerID, peer, axisVector(axis, nil), map[string]int{"rated_5": 3}, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, item := range []string{adult[0], finished[0], kids[12]} {
			if _, err := f.pool.Exec(ctx, `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 5)`, peerID, peer, item); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := f.worker.refreshProfile(ctx, userID, kid); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	leaked := map[string]string{}
	for _, id := range finished {
		leaked[id] = "finished"
	}
	for _, id := range favorited {
		leaked[id] = "favorited"
	}
	for _, id := range adult {
		leaked[id] = "above the profile's rating ceiling"
	}
	check := func(stage string, rows map[string][]string) {
		t.Helper()
		for key, ids := range rows {
			for _, id := range ids {
				if why, ok := leaked[id]; ok {
					t.Errorf("%s row %q holds %s, which is %s", stage, key, id, why)
				}
			}
		}
	}

	cached := f.cachedPersonalRows(t, userID, kid)
	// The rows were built: a main row, a Because You Watched row per anchor
	// and a Similar Users row with the open title the peers rated.
	if len(cached[RecTypeForYouMain+"|"]) < 10 {
		t.Fatalf("main row = %v, want a built row of at least 10 titles", cached[RecTypeForYouMain+"|"])
	}
	if len(cached[RecTypeBecauseWatched+"|"+finished[0]]) == 0 {
		t.Fatalf("cached rows %v, want a Because You Watched row for the latest watch", slices.Collect(maps.Keys(cached)))
	}
	if !slices.Contains(cached[RecTypeSimilarUsersLiked+"|"], kids[12]) {
		t.Errorf("Similar Users = %v, want the open title the peers rated", cached[RecTypeSimilarUsersLiked+"|"])
	}
	check("cached", cached)
	check("served", f.servedRows(t, userID, kid))
}

// Two profiles of one account with distinct histories get distinct personal
// rows: their served For You rows share at most a fifth of their titles.
// The fixture's tastes are orthogonal, so the expected overlap is none.
func TestRelevanceRubricProfilesOnOneAccountDifferPostgres(t *testing.T) {
	f := newRelevanceFixture(t, "jaccard")
	ctx := t.Context()
	const maxJaccard = 0.2

	taste := func(axis int, name string, genres [][]string) []string {
		var ids []string
		for i := range 45 {
			ids = append(ids, f.movie(t, fmt.Sprintf("%s-%02d", name, i), genres[i%len(genres)], "PG-13", 13, axisVector(axis, map[int]float32{axis + 1 + i: 0.05})))
		}
		return ids
	}
	scifi := taste(2800, "scifi", [][]string{{"Science Fiction"}, {"Thriller"}, {"Mystery"}})
	comedy := taste(2900, "comedy", [][]string{{"Comedy"}, {"Romance"}, {"Family"}})

	userID, _ := newTasteTestAccount(t, f.pool, f.prefix+"account-")
	a := f.profile(t, userID, "A", "")
	b := f.profile(t, userID, "B", "")
	f.finish(t, userID, a, scifi[:12]...)
	f.finish(t, userID, b, comedy[:12]...)
	for _, p := range []string{a, b} {
		if err := f.worker.refreshProfile(ctx, userID, p); err != nil {
			t.Fatalf("refresh %s: %v", p, err)
		}
	}

	read := func(profileID string) (main, personal []string) {
		t.Helper()
		filter := catalog.AccessFilter{UserID: userID, ProfileID: profileID}
		row, err := f.reader.GetForYouMain(ctx, userID, profileID, 20, filter)
		if err != nil {
			t.Fatal(err)
		}
		if row == nil || row.Type != "cluster" {
			t.Fatalf("profile %s main row = %+v, want a personal row", profileID, row)
		}
		page, err := f.reader.GetForYouPage(ctx, userID, profileID, 20, filter)
		if err != nil {
			t.Fatal(err)
		}
		return scoredIDs(row.Items), personalIDs(page)
	}
	mainA, personalA := read(a)
	mainB, personalB := read(b)
	if len(mainA) < 10 || len(mainB) < 10 {
		t.Fatalf("main rows hold %d and %d titles, want at least 10 each", len(mainA), len(mainB))
	}
	if j := jaccard(mainA, mainB); j > maxJaccard {
		t.Errorf("main rows overlap: Jaccard %.2f > %.2f\nA %v\nB %v", j, maxJaccard, mainA, mainB)
	}
	if j := jaccard(personalA, personalB); j > maxJaccard {
		t.Errorf("personal rows overlap: Jaccard %.2f > %.2f", j, maxJaccard)
	}
}

// A profile with no history gets a row of at least 10 titles on its first
// request, from what the worker builds at startup and nightly, with no
// refresh of its own. After a taste seed, the refresh the submission waits
// for leaves a personal row on the next read that holds none of the picks.
func TestRelevanceRubricNewProfileGetsRowsPostgres(t *testing.T) {
	f := newRelevanceFixture(t, "new")
	ctx := t.Context()
	const axis = 3000
	genres := [][]string{{"Drama"}, {"Crime"}, {"Comedy"}}
	var titles []string
	for i := range 20 {
		titles = append(titles, f.movie(t, fmt.Sprintf("title-%02d", i), genres[i%3], "PG", 8, axisVector(axis, map[int]float32{axis + 1 + i: 0.05})))
	}

	f.worker.cacheGlobalRows(ctx, f.repo, cacheExpiry(time.Now()))

	userID, _ := newTasteTestAccount(t, f.pool, f.prefix+"account-")
	newcomer := f.profile(t, userID, "New", "")
	if meta, err := f.repo.GetTasteProfileMeta(ctx, userID, newcomer); err != nil || meta != nil {
		t.Fatalf("taste profile before any signal = %+v, %v; want none", meta, err)
	}
	filter := catalog.AccessFilter{UserID: userID, ProfileID: newcomer}
	rows, err := f.reader.GetDiscoverRows(ctx, userID, newcomer, 20, filter)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(rows, func(row ForYouRow) bool { return len(row.Items) >= 10 }) {
		t.Fatalf("first Discover read = %d rows %v, want one of at least 10 titles", len(rows), rowSizes(rows))
	}

	picks := titles[:3]
	f.favorite(t, userID, newcomer, picks...)
	if !f.worker.RefreshProfileNow(ctx, userID, newcomer) {
		// A slow machine outlasted the wait; the refresh goes on.
		t.Log("refresh outlasted the wait")
		eventually(t, "the refresh to finish", func() bool { return f.worker.refreshesNow.Load() == 0 })
	}
	page, err := f.reader.GetForYouPage(ctx, userID, newcomer, 20, filter)
	if err != nil {
		t.Fatal(err)
	}
	personal := personalIDs(page)
	if len(personal) == 0 {
		t.Fatalf("page after the taste seed = %v, want a personal row", rowSizes(page))
	}
	for _, pick := range picks {
		if slices.Contains(personal, pick) {
			t.Fatalf("personal rows %v recommend pick %s back", personal, pick)
		}
	}
}

func rowSizes(rows []ForYouRow) []string {
	sizes := make([]string, len(rows))
	for i, row := range rows {
		sizes[i] = fmt.Sprintf("%s %q: %d", row.Type, row.Label, len(row.Items))
	}
	return sizes
}
