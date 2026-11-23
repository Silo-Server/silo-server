package recommendations

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
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
	return relevanceFixtureOn(pool, prefix)
}

// relevanceFixtureOn is the fixture over pool, its titles under prefix.
func relevanceFixtureOn(pool *pgxpool.Pool, prefix string) *relevanceFixture {
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
			if _, err := f.pool.Exec(ctx, `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 10)`, peerID, peer, item); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := f.worker.refreshProfile(ctx, nil, userID, kid); err != nil {
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

// The Similar Users row reads every like of the viewer and its peers, and
// matches peers by the viewer's own parental controls. Each case seeds a
// viewer, minSimilarUsersPeerAccounts peer accounts sharing its taste, and an
// open G title only the peers liked; the row holds that title exactly when the
// peers count. It takes a few seconds: each case runs a full profile refresh.
func TestSimilarUsersReadsEveryLikePostgres(t *testing.T) {
	const yearAgo = 365 * 24 * time.Hour
	type seed = similarUsersSeed
	// likesOpen is a peer that rated the open title 10 an hour ago, its
	// taste row carrying maxContentRating.
	likesOpen := func(maxContentRating string) seed {
		return func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string) {
			rateAgo(t, f, userID, profileID, it.open, 10, time.Hour)
			seedPeerTaste(t, f, userID, profileID, maxContentRating)
		}
	}
	cases := []struct {
		name    string
		ceiling string
		// viewer seeds the viewer's signals; nil finishes the liked titles.
		viewer seed
		// peer seeds one peer's signals and taste row, liking it.open.
		peer     seed
		wantOpen bool
	}{
		{
			name: "a peer's like older than its 100 newest ratings counts",
			peer: func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string) {
				rateAgo(t, f, userID, profileID, it.open, 10, yearAgo)
				rateFillerLow(t, f, userID, profileID, 100)
				seedPeerTaste(t, f, userID, profileID, "")
			},
			wantOpen: true,
		},
		{
			name: "a peer's favorite older than its 100 newest episode favorites counts",
			peer: func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string) {
				if _, err := f.pool.Exec(t.Context(), `
					INSERT INTO user_favorites(user_id, profile_id, media_item_id, added_at)
					SELECT $1::int, $2::text, $3::text, NOW() - INTERVAL '1 year'
					UNION ALL
					SELECT $1, $2, content_id, NOW() FROM media_items WHERE content_id LIKE $4`,
					userID, profileID, it.open, f.prefix+"episode-%"); err != nil {
					t.Fatal(err)
				}
				seedPeerTaste(t, f, userID, profileID, "")
			},
			wantOpen: true,
		},
		{
			name: "a viewer's like older than its 1000 newest ratings shapes its taste",
			viewer: func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string) {
				rateAgo(t, f, userID, profileID, it.liked[0], 10, yearAgo)
				rateFillerLow(t, f, userID, profileID, 1000)
			},
			peer:     likesOpen(""),
			wantOpen: true,
		},
		{
			name:     "an unrestricted viewer matches peers who took to R titles",
			peer:     likesOpen("R"),
			wantOpen: true,
		},
		{
			name:    "a PG viewer does not match peers who took to R titles",
			ceiling: "PG",
			peer:    likesOpen("R"),
		},
		{
			name: "a viewer limited by advisory age alone does not match peers who took to R titles",
			viewer: func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string) {
				f.finish(t, userID, profileID, it.liked...)
				if _, err := f.pool.Exec(t.Context(), `UPDATE user_profiles SET max_advisory_age = 10 WHERE id = $1`, profileID); err != nil {
					t.Fatal(err)
				}
			},
			peer: likesOpen("R"),
		},
		{
			name:    "an R title a peer finished and disliked does not keep it from a PG viewer",
			ceiling: "PG",
			peer: func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string) {
				f.finish(t, userID, profileID, append(slices.Clone(it.liked), it.ratedR)...)
				rateAgo(t, f, userID, profileID, it.ratedR, 2, time.Hour)
				rateAgo(t, f, userID, profileID, it.open, 10, time.Hour)
				if err := f.engine.RefreshTasteProfile(t.Context(), userID, profileID); err != nil {
					t.Fatal(err)
				}
			},
			wantOpen: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, open := similarUsersRow(t, tc.ceiling, tc.viewer, tc.peer)
			if got := slices.Contains(row, open); got != tc.wantOpen {
				t.Errorf("Similar Users = %v, holds the peers' title: %v, want %v", row, got, tc.wantOpen)
			}
		})
	}
}

// similarUsersSeed seeds the signals of one profile of a
// TestSimilarUsersReadsEveryLikePostgres case.
type similarUsersSeed func(t *testing.T, f *relevanceFixture, it similarUsersItems, userID int, profileID string)

// similarUsersRow refreshes a viewer under ceiling, seeded by viewer (nil
// finishes the liked titles), next to minSimilarUsersPeerAccounts peer
// accounts seeded by peer, and returns its cached Similar Users row and the
// open title the peers like.
func similarUsersRow(t *testing.T, ceiling string, viewer, peer similarUsersSeed) ([]string, string) {
	t.Helper()
	f := newRelevanceFixture(t, "peers")
	it := newSimilarUsersItems(t, f)
	userID, _ := newTasteTestAccount(t, f.pool, f.prefix+"viewer-")
	profileID := f.profile(t, userID, "Viewer", ceiling)
	if viewer != nil {
		viewer(t, f, it, userID, profileID)
	} else {
		f.finish(t, userID, profileID, it.liked...)
	}
	for i := range minSimilarUsersPeerAccounts {
		peerID, peerProfile := newTasteTestAccount(t, f.pool, fmt.Sprintf("%speer-%d-", f.prefix, i))
		peer(t, f, it, peerID, peerProfile)
	}
	if err := f.worker.refreshProfile(t.Context(), nil, userID, profileID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	row, ok := f.cachedPersonalRows(t, userID, profileID)[RecTypeSimilarUsersLiked+"|"]
	if !ok {
		t.Fatal("no Similar Users row was cached")
	}
	return row, it.open
}

// similarUsersTestAxis is the taste TestSimilarUsersReadsEveryLikePostgres
// gives its viewer and peers.
const similarUsersTestAxis = 2380

// similarUsersItems are the titles of a TestSimilarUsersReadsEveryLikePostgres
// case: G titles on the shared taste, an R one beside them, and the open G
// title only the peers like.
type similarUsersItems struct {
	liked        []string
	ratedR, open string
}

// newSimilarUsersItems seeds the case's titles, plus 1000 filler movies to
// rate and 100 episodes to favorite.
func newSimilarUsersItems(t *testing.T, f *relevanceFixture) similarUsersItems {
	t.Helper()
	const axis = similarUsersTestAxis
	var it similarUsersItems
	for i := range 4 {
		it.liked = append(it.liked, f.movie(t, fmt.Sprintf("liked-%d", i), []string{"Family"}, "G", 0, axisVector(axis, map[int]float32{axis + 1 + i: 0.05})))
	}
	it.ratedR = f.movie(t, "rated-r", []string{"Family"}, "R", 17, axisVector(axis, map[int]float32{axis + 10: 0.05}))
	it.open = f.movie(t, "open", []string{"Family"}, "G", 0, axisVector(axis, map[int]float32{axis + 20: 0.05}))
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO media_items(content_id, type, title, status)
		SELECT $1 || 'filler-' || g, 'movie', 'filler', 'matched' FROM generate_series(1, 1000) g
		UNION ALL
		SELECT $1 || 'episode-' || g, 'episode', 'episode', 'matched' FROM generate_series(1, 100) g`, f.prefix); err != nil {
		t.Fatal(err)
	}
	return it
}

// rateAgo stores the profile's score for the item, rated ago.
func rateAgo(t *testing.T, f *relevanceFixture, userID int, profileID, itemID string, score int, ago time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating, rated_at) VALUES($1, $2, $3, $4, NOW() - $5 * INTERVAL '1 second')`,
		userID, profileID, itemID, score, int(ago.Seconds())); err != nil {
		t.Fatal(err)
	}
}

// rateFillerLow rates n of newSimilarUsersItems' filler movies 1 star within
// the last hour.
func rateFillerLow(t *testing.T, f *relevanceFixture, userID int, profileID string, n int) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating, rated_at)
		SELECT $1, $2, $3 || 'filler-' || g, 2, NOW() - g * INTERVAL '1 second' FROM generate_series(1, $4::int) g`,
		userID, profileID, f.prefix, n); err != nil {
		t.Fatal(err)
	}
}

// seedPeerTaste stores a taste profile for a peer on similarUsersTestAxis,
// with maxContentRating as the highest rating among the titles it took to.
func seedPeerTaste(t *testing.T, f *relevanceFixture, userID int, profileID, maxContentRating string) {
	t.Helper()
	if err := f.repo.UpsertTasteProfile(t.Context(), userID, profileID, axisVector(similarUsersTestAxis, nil), map[string]int{"rated_5": 1}, maxContentRating, time.Now()); err != nil {
		t.Fatal(err)
	}
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
		if err := f.worker.refreshProfile(ctx, nil, userID, p); err != nil {
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

// Personal rows offer only recommendableMediaTypes. The fixture puts
// matched books exactly on the profile's taste, makes a finished audiobook
// its latest completion, co-watches books with its latest movie and has
// three peers rate them, so a row built from any of those paths without the
// type rule would carry one. A book still anchors its own "More like this".
func TestPersonalRowsOfferOnlyRecommendableMediaTypesPostgres(t *testing.T) {
	f := newRelevanceFixture(t, "types")
	ctx := t.Context()
	const axis = 2600
	genres := [][]string{{"Drama"}, {"Mystery"}, {"Adventure"}}

	// Enough movies that the main row's reach leaves a cluster row to build.
	var movies []string
	for i := range 70 {
		movies = append(movies, f.movie(t, fmt.Sprintf("movie-%02d", i), genres[i%3], "PG-13", 13, axisVector(axis, map[int]float32{axis + 1 + i: 0.05})))
	}
	book := func(name, mediaType string, i int) string {
		t.Helper()
		id := f.prefix + name
		seedRecoMediaItem(t, f.pool, id, mediaType, "matched")
		if _, err := f.pool.Exec(ctx, `UPDATE media_items SET genres = $2 WHERE content_id = $1`, id, genres[i%3]); err != nil {
			t.Fatal(err)
		}
		if err := f.repo.UpsertEmbedding(ctx, id, axisVector(axis, nil), "test-model", id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var books []string
	for i := range 6 {
		books = append(books, book(fmt.Sprintf("audiobook-%02d", i), "audiobook", i))
	}
	books = append(books, book("ebook", "ebook", 0))
	finishedBook := book("finished-audiobook", "audiobook", 1)

	userID, _ := newTasteTestAccount(t, f.pool, f.prefix+"account-")
	viewer := f.profile(t, userID, "Viewer", "")
	finished := movies[:12]
	// The audiobook is the latest completion; the movies follow it.
	f.finish(t, userID, viewer, append([]string{finishedBook}, finished...)...)

	// The latest movie is co-watched with an audiobook, the ebook and a movie
	// of another genre, which Because You Watched keeps, and the finished
	// audiobook with another audiobook.
	if err := f.repo.UpsertCowatchPairs(ctx, []CowatchPair{
		{ItemID: finished[0], SimilarItemID: books[0], JaccardScore: 0.9, CowatchCount: 5},
		{ItemID: finished[0], SimilarItemID: books[6], JaccardScore: 0.9, CowatchCount: 5},
		{ItemID: finished[0], SimilarItemID: movies[40], JaccardScore: 0.5, CowatchCount: 3},
		{ItemID: finishedBook, SimilarItemID: books[1], JaccardScore: 0.9, CowatchCount: 5},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(t.Context(), `DELETE FROM item_cowatch WHERE item_id LIKE $1 OR similar_item_id LIKE $1`, f.prefix+"%")
	})

	// Three peer accounts share the taste and rate two books and an open
	// movie.
	open := movies[45]
	for i := range minSimilarUsersPeerAccounts {
		peerID, peer := newTasteTestAccount(t, f.pool, fmt.Sprintf("%speer-%d-", f.prefix, i))
		if err := f.repo.UpsertTasteProfile(ctx, peerID, peer, axisVector(axis, nil), map[string]int{"rated_5": 3}, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, item := range []string{books[2], books[6], open} {
			if _, err := f.pool.Exec(ctx, `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, $3, 10)`, peerID, peer, item); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := f.worker.refreshProfile(ctx, nil, userID, viewer); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	isBook := map[string]bool{finishedBook: true}
	for _, id := range books {
		isBook[id] = true
	}
	check := func(stage string, rows map[string][]string) {
		t.Helper()
		for key, ids := range rows {
			for _, id := range ids {
				if isBook[id] {
					t.Errorf("%s row %q holds book %s", stage, key, id)
				}
			}
		}
	}

	// Candidate queries: the books are the nearest titles, and none comes
	// back, for the main row, a cluster row or a type supplement.
	filter, err := f.engine.profileAccessFilter(ctx, userID, viewer)
	if err != nil {
		t.Fatal(err)
	}
	nearest, _, err := f.repo.FindTasteProfileCandidates(ctx, axisVector(axis, nil), nil, nil, 10, filter)
	if err != nil {
		t.Fatal(err)
	}
	clustered, _, err := f.repo.FindTasteProfileCandidates(ctx, axisVector(axis, nil), nil, []string{"Drama"}, 10, filter)
	if err != nil {
		t.Fatal(err)
	}
	check("candidates", map[string][]string{"taste": scoredIDs(nearest), "genre": scoredIDs(clustered)})
	if len(nearest) != 10 || len(clustered) != 10 {
		t.Fatalf("candidates = %d and %d, want 10 movies each", len(nearest), len(clustered))
	}
	for _, mediaType := range []string{"audiobook", "ebook"} {
		extra, _, err := f.repo.FindTasteProfileCandidatesWithDetails(ctx, axisVector(axis, nil), nil, nil, 10, filter, mediaType)
		if err != nil {
			t.Fatal(err)
		}
		if len(extra) != 0 {
			t.Errorf("%s supplement = %v, want none", mediaType, scoredIDs(extra))
		}
	}

	// The finished audiobook never anchors: the anchors are the three latest
	// movies, on the SQL path and the user-store path.
	sqlAnchors, err := f.repo.GetRecentCompletedItemIDs(ctx, userID, viewer, BecauseYouWatchedAnchors)
	if err != nil {
		t.Fatal(err)
	}
	anchors, err := anchorItemIDs(ctx, f.engine.signalReader(), nil, userID, viewer, BecauseYouWatchedAnchors)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range [][]string{sqlAnchors, anchors} {
		if want := finished[:BecauseYouWatchedAnchors]; !slices.Equal(got, want) {
			t.Fatalf("anchors = %v, want the latest movies %v", got, want)
		}
	}

	cached := f.cachedPersonalRows(t, userID, viewer)
	if len(cached[RecTypeForYouMain+"|"]) < 10 {
		t.Fatalf("main row = %v, want a built row of at least 10 titles", cached[RecTypeForYouMain+"|"])
	}
	if _, ok := cached[RecTypeBecauseWatched+"|"+finishedBook]; ok {
		t.Errorf("cached rows %v hold a Because You Watched row for the finished audiobook", slices.Collect(maps.Keys(cached)))
	}
	byw := cached[RecTypeBecauseWatched+"|"+finished[0]]
	if !slices.Contains(byw, movies[40]) {
		t.Errorf("Because You Watched %s = %v, want its co-watched movie %s", finished[0], byw, movies[40])
	}
	if !slices.Contains(cached[RecTypeSimilarUsersLiked+"|"], open) {
		t.Errorf("Similar Users = %v, want the open movie the peers rated", cached[RecTypeSimilarUsersLiked+"|"])
	}
	clusterRows := 0
	for key, ids := range cached {
		if strings.HasPrefix(key, RecTypeForYouClusterPrefix) && len(ids) > 0 {
			clusterRows++
		}
	}
	if clusterRows == 0 {
		t.Errorf("cached rows %v, want a built cluster row", slices.Collect(maps.Keys(cached)))
	}
	check("cached", cached)
	check("served", f.servedRows(t, userID, viewer))

	// "More like this" keeps to the item's own type: a movie's list holds no
	// co-watched book, and an audiobook's may hold another audiobook.
	similar, err := f.engine.SimilarItems(ctx, userID, viewer, finished[0], 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	check("movie similar", map[string][]string{"similar": scoredIDs(similar)})
	bookSimilar, err := f.engine.SimilarItems(ctx, userID, viewer, finishedBook, 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(scoredIDs(bookSimilar), books[1]) {
		t.Errorf("audiobook similar = %v, want the co-watched audiobook %s", scoredIDs(bookSimilar), books[1])
	}
}

// A co-watched title the embedding search misses, because nearer titles fill
// its limit, is still scored by its similarity to the source as well as by
// co-watch. Here it is close enough, and watched together often enough, to
// lead both Because You Watched and "More like this"; with its co-watch share
// alone it would rank below every title the search found.
func TestCowatchedTitleBeyondTheEmbeddingSearchKeepsItsSimilarityPostgres(t *testing.T) {
	f := newRelevanceFixture(t, "cowatch-similarity")
	ctx := t.Context()
	const axis = 2900
	near := func(distance float32) []float32 { return axisVector(axis, map[int]float32{axis + 1: distance}) }
	source := f.movie(t, "source", []string{"Drama"}, "PG-13", 13, near(0))
	// limit 2 searches the six nearest titles; these are all nearer than
	// the co-watched one.
	for i := range 8 {
		f.movie(t, fmt.Sprintf("near-%d", i), []string{"Drama"}, "PG-13", 13, near(0.01*float32(i+1)))
	}
	cowatched := f.movie(t, "cowatched", []string{"Drama"}, "PG-13", 13, near(0.5))
	if err := f.repo.UpsertCowatchPairs(ctx, []CowatchPair{{ItemID: source, SimilarItemID: cowatched, JaccardScore: 0.9, CowatchCount: 5}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM item_cowatch WHERE item_id LIKE $1 OR similar_item_id LIKE $1`, f.prefix+"%")
	})
	userID, viewer := newTasteTestAccount(t, f.pool, f.prefix+"viewer")

	byw, err := f.engine.becauseYouWatched(ctx, f.engine.rowBuilder(), source, 2, map[string]struct{}{}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixed(byw, f.prefix); len(got) == 0 || got[0] != cowatched {
		t.Errorf("Because You Watched = %v, want the co-watched %s first", got, cowatched)
	}
	similar, err := f.engine.SimilarItems(ctx, userID, viewer, source, 2, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixed(similar, f.prefix); len(got) == 0 || got[0] != cowatched {
		t.Errorf("similar = %v, want the co-watched %s first", got, cowatched)
	}
}

// "More like this" validates every candidate against the source item, those
// the embedding search found and those only co-watch brought in: titles that
// share no genre with it are dropped, a remake of it ranks below its peers,
// and a co-watched title without an embedding keeps its place.
func TestSimilarItemsValidateEveryCandidatePostgres(t *testing.T) {
	f := newRelevanceFixture(t, "similar-validate")
	ctx := t.Context()
	const axis = 2800
	near := func(distance float32) []float32 { return axisVector(axis, map[int]float32{axis + 1: distance}) }
	setTitle := func(id, title string, year int) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, `UPDATE media_items SET title = $2, year = $3 WHERE content_id = $1`, id, title, year); err != nil {
			t.Fatal(err)
		}
	}
	cowatched := func(name string, genres []string) string {
		t.Helper()
		id := f.prefix + name
		seedRecoMediaItem(t, f.pool, id, "movie", "matched")
		if _, err := f.pool.Exec(ctx, `UPDATE media_items SET genres = $2 WHERE content_id = $1`, id, genres); err != nil {
			t.Fatal(err)
		}
		return id
	}

	source := f.movie(t, "source", []string{"Drama"}, "PG-13", 13, near(0))
	setTitle(source, "Harbor Lights", 2020)
	remake := f.movie(t, "remake", []string{"Drama"}, "PG-13", 13, near(0.01))
	setTitle(remake, "Harbor Lights", 1990)
	comedy := f.movie(t, "comedy", []string{"Comedy"}, "PG-13", 13, near(0.02))
	var peers []string
	for i := range 3 {
		peers = append(peers, f.movie(t, fmt.Sprintf("peer-%d", i), []string{"Drama"}, "PG-13", 13, near(0.03+0.01*float32(i))))
	}
	drama, otherComedy := cowatched("cowatched-drama", []string{"Drama"}), cowatched("cowatched-comedy", []string{"Comedy"})
	if err := f.repo.UpsertCowatchPairs(ctx, []CowatchPair{
		{ItemID: source, SimilarItemID: drama, JaccardScore: 0.9, CowatchCount: 5},
		{ItemID: source, SimilarItemID: otherComedy, JaccardScore: 0.9, CowatchCount: 5},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM item_cowatch WHERE item_id LIKE $1 OR similar_item_id LIKE $1`, f.prefix+"%")
	})
	userID, viewer := newTasteTestAccount(t, f.pool, f.prefix+"viewer")

	items, err := f.engine.SimilarItems(ctx, userID, viewer, source, 10, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := prefixed(items, f.prefix)
	if slices.Contains(got, comedy) || slices.Contains(got, otherComedy) {
		t.Errorf("similar = %v, want no title without a shared genre", got)
	}
	if !slices.Contains(got, drama) {
		t.Errorf("similar = %v, want the co-watched %s without an embedding", got, drama)
	}
	at := slices.Index(got, remake)
	for _, peer := range peers {
		if i := slices.Index(got, peer); i < 0 || at < i {
			t.Errorf("similar = %v, want every peer, and the remake after them", got)
			break
		}
	}
}
