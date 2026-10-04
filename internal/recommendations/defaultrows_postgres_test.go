package recommendations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// newTestLibrary creates a movies library that the test removes afterwards.
func newTestLibrary(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id) })
	return id
}

// seedTitle seeds a matched title in library (none when 0) and applies set,
// an UPDATE SET list over its media_items row. It returns the content ID.
func seedTitle(t *testing.T, pool *pgxpool.Pool, id, mediaType string, library int, set string) string {
	t.Helper()
	seedRecoMediaItem(t, pool, id, mediaType, "matched")
	if set != "" {
		if _, err := pool.Exec(t.Context(), `UPDATE media_items SET `+set+` WHERE content_id = $1`, id); err != nil {
			t.Fatalf("update %s: %v", id, err)
		}
	}
	if library != 0 {
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library); err != nil {
			t.Fatalf("library of %s: %v", id, err)
		}
	}
	return id
}

// watch records a completed watch of itemID by a profile, ago before now.
func watch(t *testing.T, pool *pgxpool.Pool, userID int, profileID, itemID string, ago time.Duration) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, completed, updated_at) VALUES ($1, $2, $3, true, NOW() - make_interval(secs => $4))`, userID, profileID, itemID, ago.Seconds()); err != nil {
		t.Fatalf("watch %s: %v", itemID, err)
	}
}

// addProfile adds a second profile to an account.
func addProfile(t *testing.T, pool *pgxpool.Pool, userID int, profileID string) string {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $1)`, profileID, userID); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	return profileID
}

func itemIDs(items []ScoredItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.MediaItemID
	}
	return ids
}

func listDefault(t *testing.T, repo *Repo, filter catalog.AccessFilter, kind string, exclude []string, limit int) []string {
	t.Helper()
	items, err := repo.ListDefaultRowItems(t.Context(), filter, kind, exclude, limit)
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return itemIDs(items)
}

// Highly Rated holds the matched movies and series the viewer can see with a
// catalog rating of 7.0 or more, IMDb-backed titles before TMDB-only ones and
// an implausible TMDB rating not trusted, under the viewer's access.
func TestHighlyRatedRowPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tdefault-rated-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	open, adult := newTestLibrary(t, pool, prefix+"open"), newTestLibrary(t, pool, prefix+"adult")
	const kids = "content_rating = 'G', content_rating_age = 0, "

	adultBest := seedTitle(t, pool, prefix+"adult-90", "movie", adult, "content_rating = 'R', content_rating_age = 17, rating_imdb = 9.0")
	best := seedTitle(t, pool, prefix+"imdb-85", "movie", open, kids+"rating_imdb = 8.5")
	low := seedTitle(t, pool, prefix+"imdb-72", "movie", open, kids+"rating_imdb = 7.2")
	tmdbOnly := seedTitle(t, pool, prefix+"tmdb-80", "movie", open, kids+"rating_tmdb = 8.0")
	seedTitle(t, pool, prefix+"tmdb-98", "movie", open, kids+"rating_tmdb = 9.8")
	seedTitle(t, pool, prefix+"imdb-65", "movie", open, kids+"rating_imdb = 6.5")
	series := seedTitle(t, pool, prefix+"series-88", "series", open, kids+"rating_imdb = 8.8")
	seedTitle(t, pool, prefix+"ebook-95", "ebook", open, kids+"rating_imdb = 9.5")
	seedTitle(t, pool, prefix+"audiobook-95", "audiobook", open, kids+"rating_imdb = 9.5")
	seedTitle(t, pool, prefix+"unmatched-97", "movie", open, kids+"rating_imdb = 9.7, status = 'unmatched'")

	both := catalog.AccessFilter{AllowedLibraryIDs: []int{open, adult}}
	got := listDefault(t, repo, both, RecTypeTopRated, nil, 60)
	// Movies outnumber series six to one in what the viewer can see, so the
	// one series comes after the movies.
	if want := []string{adultBest, best, low, tmdbOnly, series}; !slices.Equal(got, want) {
		t.Fatalf("highly rated = %v, want %v", got, want)
	}
	types, err := repo.GetItemMediaTypes(t.Context(), got)
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, mediaType := range types {
		offered[mediaType] = true
	}
	for _, mediaType := range recommendableMediaTypes {
		if !offered[mediaType] {
			t.Fatalf("highly rated offers no %s: %v", mediaType, types)
		}
	}

	kidsOnly := []string{best, low, tmdbOnly, series}
	for name, filter := range map[string]catalog.AccessFilter{
		"allowed library": {AllowedLibraryIDs: []int{open}},
		"hidden library":  {AllowedLibraryIDs: []int{open, adult}, DisabledLibraryIDs: []int{adult}},
		"G/PG ceiling":    {AllowedLibraryIDs: []int{open, adult}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}},
	} {
		if got := listDefault(t, repo, filter, RecTypeTopRated, nil, 60); !slices.Equal(got, kidsOnly) {
			t.Fatalf("%s: highly rated = %v, want %v", name, got, kidsOnly)
		}
	}

	if got := listDefault(t, repo, both, RecTypeTopRated, []string{adultBest, low}, 60); !slices.Equal(got, []string{best, tmdbOnly, series}) {
		t.Fatalf("with exclusions = %v", got)
	}
	if got := listDefault(t, repo, both, RecTypeTopRated, nil, 2); !slices.Equal(got, []string{adultBest, best}) {
		t.Fatalf("limit 2 = %v", got)
	}
	allowed := catalog.AccessFilter{AllowedLibraryIDs: []int{open, adult}, AllowedContentIDs: []string{series, low}}
	if got := listDefault(t, repo, allowed, RecTypeTopRated, nil, 60); !slices.Equal(got, []string{series, low}) {
		t.Fatalf("allowed content = %v", got)
	}
	for name, filter := range map[string]catalog.AccessFilter{
		"no library": {AllowedLibraryIDs: []int{}},
		"no content": {AllowedContentIDs: []string{}},
	} {
		if got := listDefault(t, repo, filter, RecTypeTopRated, nil, 60); len(got) != 0 {
			t.Fatalf("%s: %v, want nothing", name, got)
		}
	}
}

// A library of nine movies and three series gives series about a quarter of
// Highly Rated even when every series outrates every movie.
func TestHighlyRatedRowBalancesMediaTypesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tdefault-balance-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	lib := newTestLibrary(t, pool, prefix+"lib")
	series := map[string]bool{}
	for i := range 9 {
		seedTitle(t, pool, fmt.Sprintf("%smovie-%d", prefix, i), "movie", lib, fmt.Sprintf("rating_imdb = %.1f", 7.0+float64(i)/10))
	}
	for i := range 3 {
		series[seedTitle(t, pool, fmt.Sprintf("%sseries-%d", prefix, i), "series", lib, fmt.Sprintf("rating_imdb = %.1f", 9.0+float64(i)/10))] = true
	}

	got := listDefault(t, repo, catalog.AccessFilter{AllowedLibraryIDs: []int{lib}}, RecTypeTopRated, nil, 60)
	if len(got) != 12 {
		t.Fatalf("highly rated = %v, want all 12 titles", got)
	}
	seriesIn := func(n int) int {
		count := 0
		for _, id := range got[:n] {
			if series[id] {
				count++
			}
		}
		return count
	}
	if seriesIn(4) != 1 || seriesIn(8) != 2 {
		t.Fatalf("series in the first 4 = %d and 8 = %d, want 1 and 2: %v", seriesIn(4), seriesIn(8), got)
	}
}

// Recently Added orders matched movies and series by when a title, or a
// series' latest episode, was added, and breaks ties by content ID. The live
// row and the cached global row share that order and have no window; the
// live /recently-added endpoint still honors its days.
func TestRecentlyAddedRowPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tdefault-recent-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	lib := newTestLibrary(t, pool, prefix+"lib")
	added := func(daysAgo int) string {
		return fmt.Sprintf("created_at = NOW() - INTERVAL '%d days'", daysAgo)
	}
	old := seedTitle(t, pool, prefix+"old", "movie", lib, added(400))
	mid := seedTitle(t, pool, prefix+"mid", "movie", lib, added(20))
	series := seedTitle(t, pool, prefix+"series", "series", lib, added(300)+", latest_episode_added_at = NOW() - INTERVAL '1 day'")
	newest := seedTitle(t, pool, prefix+"new", "movie", lib, added(2))
	tieA := seedTitle(t, pool, prefix+"tie-a", "movie", lib, "created_at = TIMESTAMPTZ '2026-09-28 12:00:00Z'")
	tieB := seedTitle(t, pool, prefix+"tie-b", "movie", lib, "created_at = TIMESTAMPTZ '2026-09-28 12:00:00Z'")
	seedTitle(t, pool, prefix+"ebook", "ebook", lib, added(0))
	seedTitle(t, pool, prefix+"unmatched", "movie", lib, added(0)+", status = 'unmatched'")

	live := listDefault(t, repo, catalog.AccessFilter{AllowedLibraryIDs: []int{lib}}, RecTypeRecentlyAdded, nil, 60)
	// The tied pair's fixed date sorts it among the others by how long ago
	// the test runs; only its own order is asserted.
	without := slices.DeleteFunc(slices.Clone(live), func(id string) bool { return id == tieA || id == tieB })
	if want := []string{series, newest, mid, old}; !slices.Equal(without, want) {
		t.Fatalf("recently added = %v, want %v around the tied pair", live, want)
	}
	if a, b := slices.Index(live, tieA), slices.Index(live, tieB); a < 0 || b != a+1 {
		t.Fatalf("recently added = %v, want %s directly before %s", live, tieA, tieB)
	}

	cachedRow := func(days int) []string {
		t.Helper()
		items, err := repo.GetRecentlyAddedItems(t.Context(), days, 100000)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, item := range items {
			if strings.HasPrefix(item.MediaItemID, prefix) {
				ids = append(ids, item.MediaItemID)
			}
		}
		return ids
	}
	if got := cachedRow(0); !slices.Equal(got, live) {
		t.Fatalf("cached row = %v, want the live row's order %v", got, live)
	}
	// An explicit window keeps titles, or series episodes, added within it.
	windowed := cachedRow(14)
	if slices.Contains(windowed, old) || slices.Contains(windowed, mid) || !slices.Contains(windowed, series) || !slices.Contains(windowed, newest) {
		t.Fatalf("14-day row = %v, want the series and the 2-day-old title, not the 20- and 400-day-old ones", windowed)
	}
}

// The cached Popular row counts login accounts, not profiles, over a 90-day
// window, and offers only matched movies and series.
func TestPopularNeedsACrowdOfAccountsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tpopular-crowd-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	userA, a1 := newTasteTestAccount(t, pool, prefix+"a")
	a2 := addProfile(t, pool, userA, "second-"+a1)
	userB, b1 := newTasteTestAccount(t, pool, prefix+"b")
	userC, c1 := newTasteTestAccount(t, pool, prefix+"c")

	three := seedTitle(t, pool, prefix+"three", "movie", 0, "")
	sharedRecent := seedTitle(t, pool, prefix+"shared-recent", "series", 0, "")
	sharedOlder := seedTitle(t, pool, prefix+"shared-older", "movie", 0, "")
	household := seedTitle(t, pool, prefix+"household", "movie", 0, "")
	stale := seedTitle(t, pool, prefix+"stale", "movie", 0, "")
	book := seedTitle(t, pool, prefix+"audiobook", "audiobook", 0, "")
	unmatched := seedTitle(t, pool, prefix+"unmatched", "movie", 0, "status = 'unmatched'")

	for _, w := range []struct {
		user    int
		profile string
		item    string
		ago     time.Duration
	}{
		{userA, a1, three, 3 * time.Hour}, {userB, b1, three, 3 * time.Hour}, {userC, c1, three, 3 * time.Hour},
		{userA, a1, sharedRecent, time.Hour}, {userB, b1, sharedRecent, time.Hour},
		{userA, a1, sharedOlder, 48 * time.Hour}, {userB, b1, sharedOlder, 48 * time.Hour},
		{userA, a1, household, 0}, {userA, a2, household, 0},
		{userA, a1, stale, 100 * 24 * time.Hour}, {userB, b1, stale, 100 * 24 * time.Hour},
		{userA, a1, book, 0}, {userB, b1, book, 0},
		{userA, a1, unmatched, 0}, {userB, b1, unmatched, 0},
	} {
		watch(t, pool, w.user, w.profile, w.item, w.ago)
	}

	popular := func(minAccounts int) ([]string, []float64) {
		t.Helper()
		items, err := repo.GetPopularItems(t.Context(), 90, minAccounts, 100000)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		var scores []float64
		for _, item := range items {
			if strings.HasPrefix(item.MediaItemID, prefix) {
				ids = append(ids, item.MediaItemID)
				scores = append(scores, item.Score)
			}
		}
		return ids, scores
	}
	ids, scores := popular(crowdMinAccounts)
	if want := []string{three, sharedRecent, sharedOlder}; !slices.Equal(ids, want) {
		t.Fatalf("popular = %v, want %v", ids, want)
	}
	if !slices.Equal(scores, []float64{3, 2, 2}) {
		t.Fatalf("scores = %v, want account counts", scores)
	}
	// The live endpoint keeps a floor of one account: two profiles of one
	// household are still one account.
	if ids, scores := popular(1); !slices.Equal(ids, []string{three, sharedRecent, sharedOlder, household}) || scores[3] != 1 {
		t.Fatalf("popular from one account = %v %v", ids, scores)
	}
}

// Genres rank by how many accounts watched them, once at least two did, then
// by how many movies and series they hold; book genres do not count.
func TestTopGenresRankByCrowdThenCatalogPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tgenres-menu-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	userA, a1 := newTasteTestAccount(t, pool, prefix+"a")
	a2 := addProfile(t, pool, userA, "second-"+a1)
	userB, b1 := newTasteTestAccount(t, pool, prefix+"b")
	crowd, household, big, books := prefix+"Crowd", prefix+"Household", prefix+"Big", prefix+"Books"
	genre := func(name string) string { return fmt.Sprintf("genres = ARRAY['%s']", name) }

	crowdTitle := seedTitle(t, pool, prefix+"crowd", "movie", 0, genre(crowd))
	watch(t, pool, userA, a1, crowdTitle, 0)
	watch(t, pool, userB, b1, crowdTitle, 0)
	for i := range 3 {
		id := seedTitle(t, pool, fmt.Sprintf("%shousehold-%d", prefix, i), "series", 0, genre(household))
		watch(t, pool, userA, a1, id, 0)
		watch(t, pool, userA, a2, id, 0)
	}
	for i := range 5 {
		seedTitle(t, pool, fmt.Sprintf("%sbig-%d", prefix, i), "movie", 0, genre(big))
	}
	for i := range 8 {
		id := seedTitle(t, pool, fmt.Sprintf("%sbook-%d", prefix, i), "ebook", 0, genre(books))
		watch(t, pool, userA, a1, id, 0)
		watch(t, pool, userB, b1, id, 0)
	}

	all, err := repo.GetTopGenres(t.Context(), 100000)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range all {
		if strings.HasPrefix(g, prefix) {
			got = append(got, g)
		}
	}
	// One household's viewing does not lift its genre above a larger one.
	if want := []string{crowd, big, household}; !slices.Equal(got, want) {
		t.Fatalf("top genres = %v, want %v", got, want)
	}
}

// A genre row ranks its titles by rating reliability and rating, then by
// how many accounts watched them, then newest first; one household's
// viewing does not count, and books are left out.
func TestGenreSamplerIsRatingLedPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tgenres-sampler-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	userA, a1 := newTasteTestAccount(t, pool, prefix+"a")
	a2 := addProfile(t, pool, userA, "second-"+a1)
	userB, b1 := newTasteTestAccount(t, pool, prefix+"b")
	g := prefix + "Genre"
	in := fmt.Sprintf("genres = ARRAY['%s'], ", g)

	best := seedTitle(t, pool, prefix+"imdb-80", "movie", 0, in+"rating_imdb = 8.0")
	crowd := seedTitle(t, pool, prefix+"crowd", "movie", 0, in+"rating_imdb = 7.0, year = 2000")
	household := seedTitle(t, pool, prefix+"household", "series", 0, in+"rating_imdb = 7.0, year = 2000")
	newer := seedTitle(t, pool, prefix+"newer", "movie", 0, in+"rating_imdb = 7.0, year = 2020")
	older := seedTitle(t, pool, prefix+"older", "movie", 0, in+"rating_imdb = 7.0, year = 2010")
	tmdb := seedTitle(t, pool, prefix+"tmdb-85", "movie", 0, in+"rating_tmdb = 8.5")
	untrusted := seedTitle(t, pool, prefix+"tmdb-99", "movie", 0, in+"rating_tmdb = 9.9")
	seedTitle(t, pool, prefix+"ebook", "ebook", 0, in+"rating_imdb = 9.0")
	watch(t, pool, userA, a1, crowd, 0)
	watch(t, pool, userB, b1, crowd, 0)
	watch(t, pool, userA, a1, household, 0)
	watch(t, pool, userA, a2, household, 0)

	items, err := repo.GetGenreSamplerItems(t.Context(), g, 60)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{best, crowd, newer, older, household, tmdb, untrusted}; !slices.Equal(itemIDs(items), want) {
		t.Fatalf("genre row = %v, want %v", itemIDs(items), want)
	}
	if items[0].Score != 8.0 {
		t.Fatalf("score = %v, want the catalog rating", items[0].Score)
	}
}

// The picker's best 600 candidates come first, interleaved by first genre;
// paging then continues through the rest in rank order, every candidate
// exactly once, and only recommendable media types are offered.
func TestTasteSeedPickerInterleavesGenresPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "ttasteseed-rounds-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	lib := newTestLibrary(t, pool, prefix+"lib")

	// Movies rank in index order behind one series: movies 0-399 are Alpha,
	// 400-599 Beta and 600-639 Gamma.
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, poster_path, rating_imdb, genres)
		SELECT $1 || lpad(i::text, 4, '0'), 'movie', 'm' || i, 'matched', '/p.jpg', 9.0 - i * 0.001,
		       ARRAY[CASE WHEN i < 400 THEN 'Alpha' WHEN i < 600 THEN 'Beta' ELSE 'Gamma' END]
		FROM generate_series(0, 639) AS i`, prefix+"movie-"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT content_id, $2 FROM media_items WHERE content_id LIKE $1`, prefix+"movie-%", lib); err != nil {
		t.Fatal(err)
	}
	movie := func(i int) string { return fmt.Sprintf("%smovie-%04d", prefix, i) }
	series := seedTitle(t, pool, prefix+"series", "series", lib, "poster_path = '/p.jpg', rating_imdb = 9.5, genres = ARRAY['Alpha']")
	for _, mediaType := range []string{"ebook", "audiobook"} {
		seedTitle(t, pool, prefix+mediaType, mediaType, lib, "poster_path = '/p.jpg', rating_imdb = 9.9, genres = ARRAY['Books']")
	}
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{lib}}

	page, err := repo.GetTasteSeedCandidates(ctx, filter, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Alpha's best (the series), Beta's best, then each genre's next.
	if want := []string{series, movie(400), movie(0), movie(401), movie(1), movie(402)}; !slices.Equal(page, want) {
		t.Fatalf("page 1 = %v, want %v", page, want)
	}

	var all []string
	for offset := 0; ; offset += 100 {
		page, err := repo.GetTasteSeedCandidates(ctx, filter, 100, offset)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if len(page) < 100 {
			break
		}
	}
	if len(all) != 641 {
		t.Fatalf("paged %d candidates, want the 640 movies and the series", len(all))
	}
	seen := map[string]bool{}
	for _, id := range all {
		if seen[id] {
			t.Fatalf("%s paged twice", id)
		}
		seen[id] = true
	}
	// The series and movies 0-598 fill the base; movies 599 and on were not
	// interleaved and follow in rank order.
	for i, id := range all[600:] {
		if id != movie(599+i) {
			t.Fatalf("candidate %d = %s, want %s", 600+i, id, movie(599+i))
		}
	}
	types, err := repo.GetItemMediaTypes(ctx, all)
	if err != nil {
		t.Fatal(err)
	}
	for id, mediaType := range types {
		if !slices.Contains(recommendableMediaTypes, mediaType) {
			t.Fatalf("picker offered %s, a %s", id, mediaType)
		}
	}
}

// The default rows are queried for each viewer at read time: a restricted
// profile gets titles it can see, each profile loses only what it watched,
// and the rows show with recommendations disabled (no refresher) on a server
// that has cached nothing, without writing to the cache.
func TestDefaultRowsServeEachViewerLivePostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tdefault-viewer-"
	cleanupRecoMediaItems(t, pool, prefix)
	open, adult := newTestLibrary(t, pool, prefix+"open"), newTestLibrary(t, pool, prefix+"adult")
	userID, grownUp := newTasteTestAccount(t, pool, prefix)
	kid := addProfile(t, pool, userID, "kid-"+grownUp)
	const kids = "content_rating = 'G', content_rating_age = 0, "

	adultTitle := seedTitle(t, pool, prefix+"adult", "movie", adult, "content_rating = 'R', content_rating_age = 17, rating_imdb = 9.0")
	watchedByGrownUp := seedTitle(t, pool, prefix+"family-88", "movie", open, kids+"rating_imdb = 8.8")
	family := seedTitle(t, pool, prefix+"family-85", "movie", open, kids+"rating_imdb = 8.5")
	// Unrated, so only Recently Added holds it.
	seedTitle(t, pool, prefix+"unrated", "movie", open, strings.TrimSuffix(kids, ", "))
	watch(t, pool, userID, grownUp, watchedByGrownUp, 0)

	grownUpFilter := catalog.AccessFilter{UserID: userID, ProfileID: grownUp, AllowedLibraryIDs: []int{open, adult}}
	kidFilter := catalog.AccessFilter{UserID: userID, ProfileID: kid, AllowedLibraryIDs: []int{open}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	cacheRows := func() int {
		return countRows(t, pool, `SELECT count(*) FROM recommendation_cache WHERE rec_type = $1 OR user_id = $2`, RecTypeTopRated, userID)
	}
	before := cacheRows()

	r := NewReader(NewRepo(pool), nil, nil, nil)
	section := func(profile string, filter catalog.AccessFilter) []string {
		t.Helper()
		row, err := r.GetSection(ctx, userID, profile, SectionKindTopRated, "", 60, filter)
		if err != nil {
			t.Fatal(err)
		}
		if row == nil || row.Type != RecTypeTopRated || row.Label != highlyRatedLabel {
			t.Fatalf("section = %+v", row)
		}
		return itemIDs(row.Items)
	}
	if got := section(grownUp, grownUpFilter); !slices.Equal(got, []string{adultTitle, family}) {
		t.Fatalf("grown-up highly rated = %v", got)
	}
	if got := section(kid, kidFilter); !slices.Equal(got, []string{watchedByGrownUp, family}) {
		t.Fatalf("kid highly rated = %v", got)
	}

	rows, err := r.GetDiscoverRows(ctx, userID, kid, 20, kidFilter)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, row := range rows {
		labels = append(labels, row.Label)
		for _, item := range row.Items {
			if item.MediaItemID == adultTitle {
				t.Fatalf("kid's %q row shows the adult title", row.Label)
			}
		}
	}
	if !slices.Contains(labels, highlyRatedLabel) || !slices.Contains(labels, recentlyAddedLabel) {
		t.Fatalf("kid's discover rows = %v, want both default rows", labels)
	}
	if after := cacheRows(); after != before {
		t.Fatalf("reads wrote %d cache rows", after-before)
	}
}

// Each default row reads every media type from its own index, newest or best
// rated first, rather than scanning and sorting the catalog; a read then
// costs the same however many titles the server holds. Sequential scans and
// sorts are disabled only in this transaction because the test catalog is too
// small to favor an index naturally; only the type indexes supply the order
// without a sort.
func TestDefaultRowQueriesWalkTypeIndexesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, setting := range []string{"enable_seqscan", "enable_sort"} {
		if _, err := tx.Exec(ctx, "SET LOCAL "+setting+" = off"); err != nil {
			t.Fatal(err)
		}
	}
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	exclude := []string{"watched-1", "watched-2"}
	for kind, index := range map[string]string{
		RecTypeRecentlyAdded: "idx_media_items_type_added_at",
		RecTypeTopRated:      "idx_media_items_type_catalog_rating",
	} {
		query, args := defaultRowQuery(filter, kind, exclude, []int64{9, 3}, 60)
		var plan []byte
		if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		var nodes []map[string]any
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatalf("%s: plan: %v", kind, err)
		}
		reads := mediaItemsReads(nodes[0]["Plan"].(map[string]any))
		if len(reads) == 0 {
			t.Fatalf("%s reads no media_items: %s", kind, plan)
		}
		for _, read := range reads {
			if read != "Index Scan "+index {
				t.Fatalf("%s reads media_items by %q, want an index scan of %s: %s", kind, read, index, plan)
			}
		}
	}
}

// mediaItemsReads lists how a plan reads media_items: each scan's node type
// and index.
func mediaItemsReads(node map[string]any) []string {
	var reads []string
	if node["Relation Name"] == "media_items" {
		index, _ := node["Index Name"].(string)
		reads = append(reads, strings.TrimSpace(fmt.Sprintf("%v %s", node["Node Type"], index)))
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		reads = append(reads, mediaItemsReads(child.(map[string]any))...)
	}
	return reads
}

// Highly Rated reuses an access scope's title counts for a while instead of
// counting the catalog on every read; another scope is counted on its own.
func TestHighlyRatedReusesTitleCountsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	const prefix = "tdefault-counts-"
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	lib, other := newTestLibrary(t, pool, prefix+"lib"), newTestLibrary(t, pool, prefix+"other")
	seedTitle(t, pool, prefix+"movie-1", "movie", lib, "")
	seedTitle(t, pool, prefix+"series-1", "series", lib, "")
	scope := catalog.AccessFilter{AllowedLibraryIDs: []int{lib}}
	counts := func(filter catalog.AccessFilter) []int64 {
		t.Helper()
		got, err := repo.titleCounts.get(t.Context(), repo, filter)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := counts(scope); !slices.Equal(got, []int64{1, 1}) {
		t.Fatalf("counts = %v, want one movie and one series", got)
	}
	seedTitle(t, pool, prefix+"movie-2", "movie", lib, "")
	seedTitle(t, pool, prefix+"movie-3", "movie", other, "")
	if got := counts(scope); !slices.Equal(got, []int64{1, 1}) {
		t.Fatalf("counts = %v, want the scope's earlier counts", got)
	}
	if got := counts(catalog.AccessFilter{AllowedLibraryIDs: []int{lib, other}}); !slices.Equal(got, []int64{3, 1}) {
		t.Fatalf("another scope's counts = %v, want 3 movies and 1 series", got)
	}
}
