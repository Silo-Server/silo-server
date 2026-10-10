package shuffle

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/migrations"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(t.Context(), pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

// fixture seeds a TV library, a movie library, and one profile, all named
// with a unique prefix so tests can share a database.
type fixture struct {
	pool      *pgxpool.Pool
	prefix    string
	owner     Owner
	tvLibrary int
	movies    int
	series    string
	// Regular episodes by season, plus a special and an episode whose file
	// is missing.
	season1  []string
	season2  []string
	special  string
	missing  string
	movieOK  string
	movieR   string
	movieOff string
}

func seed(t *testing.T) *fixture {
	t.Helper()
	pool := testPool(t)
	ctx := t.Context()
	f := &fixture{pool: pool, prefix: fmt.Sprintf("shuffle-%d-", time.Now().UnixNano())}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var items []string
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, f.owner.UserID)
		_, _ = pool.Exec(cleanup, `DELETE FROM episodes WHERE series_id = $1`, f.series)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = ANY($1)`, items)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{f.tvLibrary, f.movies})
	})

	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, f.prefix+"user").Scan(&f.owner.UserID); err != nil {
		t.Fatal(err)
	}
	f.owner.ProfileID = f.prefix + "profile"
	exec(`INSERT INTO user_profiles (user_id, id, name) VALUES ($1, $2, 'Profile')`, f.owner.UserID, f.owner.ProfileID)

	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, f.prefix+"tv").Scan(&f.tvLibrary); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, f.prefix+"movies").Scan(&f.movies); err != nil {
		t.Fatal(err)
	}

	f.series = f.prefix + "series"
	items = append(items, f.series)
	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Show')`, f.series)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, f.series, f.tvLibrary)
	exec(`INSERT INTO seasons (content_id, series_id, season_number, title) VALUES ($1, $2, 2, 'Second Season')`, f.prefix+"season-2", f.series)
	episode := func(season, number int, present bool) string {
		id := fmt.Sprintf("%sep-%d-%d", f.prefix, season, number)
		exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, $3, $4, $1)`, id, f.series, season, number)
		var missing *time.Time
		if !present {
			now := time.Now()
			missing = &now
		}
		exec(`INSERT INTO media_files (episode_id, content_id, media_folder_id, file_path, missing_since) VALUES ($1, $2, $3, $4, $5)`,
			id, f.series, f.tvLibrary, "/tv/"+id+".mkv", missing)
		return id
	}
	f.season1 = []string{episode(1, 1, true), episode(1, 2, true)}
	f.season2 = []string{episode(2, 1, true), episode(2, 2, true)}
	f.special = episode(0, 1, true)
	f.missing = episode(1, 3, false)

	movie := func(name string, ratingAge *int, present bool) string {
		id := f.prefix + name
		items = append(items, id)
		exec(`INSERT INTO media_items (content_id, type, title, content_rating_age) VALUES ($1, 'movie', $1, $2)`, id, ratingAge)
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, f.movies)
		var missing *time.Time
		if !present {
			now := time.Now()
			missing = &now
		}
		exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, missing_since) VALUES ($1, $2, $3, $4)`,
			id, f.movies, "/movies/"+id+".mkv", missing)
		return id
	}
	pg := 13
	r := 17
	f.movieOK = movie("movie-pg13", &pg, true)
	f.movieR = movie("movie-r", &r, true)
	f.movieOff = movie("movie-missing", &pg, false)
	setSampleIDRange(t, fmt.Sprintf(`SELECT min(id) AS lo, max(id) AS hi FROM media_files WHERE media_folder_id IN (%d, %d)`, f.tvLibrary, f.movies))
	return f
}

// setSampleIDRange replaces sampleIDRange for the rest of the test. seed
// narrows it to the fixture's own files, so library picks in every test
// exercise sampling rather than only the full read.
func setSampleIDRange(t *testing.T, sql string) {
	t.Helper()
	previous := sampleIDRange
	sampleIDRange = sql
	t.Cleanup(func() { sampleIDRange = previous })
}

func (f *fixture) library(id int) Scope { return Scope{Kind: ScopeLibrary, ID: strconv.Itoa(id)} }

// allEpisodes is every playable episode: both seasons and the special.
func (f *fixture) allEpisodes() []string {
	return append(append(append([]string{}, f.season1...), f.season2...), f.special)
}

// playthrough advances n times and returns every current item in order,
// starting with the shuffle's first.
func playthrough(t *testing.T, svc *Service, owner Owner, filter catalog.AccessFilter, s *Shuffle, n int) []string {
	t.Helper()
	played := []string{s.CurrentContentID}
	for range n {
		next := s.NextContentID
		advanced, err := svc.Advance(t.Context(), owner, filter, s.ID, s.CurrentContentID)
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if advanced.CurrentContentID != next {
			t.Fatalf("advance played %q, want the announced next %q", advanced.CurrentContentID, next)
		}
		s = advanced
		played = append(played, s.CurrentContentID)
	}
	return played
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] != 1 {
			return false
		}
	}
	return true
}

func TestLibraryShufflePlaysEveryEpisodeBeforeRepeating(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()

	s, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, f.library(f.tvLibrary))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != f.prefix+"tv" {
		t.Fatalf("title = %q", s.Title)
	}
	all := f.allEpisodes()
	played := playthrough(t, svc, f.owner, catalog.AccessFilter{}, s, 9)
	if first := played[:5]; !sameSet(first, all) {
		t.Fatalf("first cycle %v, want each of %v once (specials included, no missing files)", first, all)
	}
	if second := played[5:10]; !sameSet(second, all) {
		t.Fatalf("second cycle %v, want each of %v once", second, all)
	}
	if played[5] == played[4] {
		t.Fatalf("new cycle opened with the item that just played: %v", played)
	}
}

func TestSeasonShufflePlaysOnlyThatSeason(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()

	s, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeSeason, ID: f.prefix + "season-2"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Second Season" || s.ParentTitle != "Show" {
		t.Fatalf("titles = %q / %q", s.Title, s.ParentTitle)
	}
	if got := playthrough(t, svc, f.owner, catalog.AccessFilter{}, s, 1); !sameSet(got, f.season2) {
		t.Fatalf("played %v, want %v", got, f.season2)
	}

	// A series without stored season metadata names its seasons
	// "<series>-S<number>"; season 0 is the specials, which play here.
	specials, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeSeason, ID: f.series + "-S0"})
	if err != nil {
		t.Fatal(err)
	}
	if specials.Title != "Specials" || specials.CurrentContentID != f.special || specials.NextContentID != f.special {
		t.Fatalf("specials shuffle = %+v", specials)
	}
}

func TestSeriesShuffleIncludesSpecials(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	s, err := svc.Create(t.Context(), f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	if got := playthrough(t, svc, f.owner, catalog.AccessFilter{}, s, 4); !sameSet(got, f.allEpisodes()) {
		t.Fatalf("played %v, want %v", got, f.allEpisodes())
	}
}

func TestMovieShuffleAppliesParentalLimitsAndMissingFiles(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()

	unrestricted, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, f.library(f.movies))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{unrestricted.CurrentContentID, unrestricted.NextContentID}; !sameSet(got, []string{f.movieOK, f.movieR}) {
		t.Fatalf("unrestricted picks %v", got)
	}

	limited := catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	s, err := svc.Create(ctx, f.owner, limited, f.library(f.movies))
	if err != nil {
		t.Fatal(err)
	}
	// One playable movie: it plays again rather than ending the shuffle.
	if s.CurrentContentID != f.movieOK || s.NextContentID != f.movieOK {
		t.Fatalf("limited shuffle = %+v", s)
	}
	if got := playthrough(t, svc, f.owner, limited, s, 2); !sameSet(got[:1], []string{f.movieOK}) || got[2] != f.movieOK {
		t.Fatalf("limited playthrough %v", got)
	}
}

func TestShuffleRespectsLibraryAccessAndKinds(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()

	if _, err := svc.Create(ctx, f.owner, catalog.AccessFilter{AllowedLibraryIDs: []int{f.movies}}, f.library(f.tvLibrary)); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("library outside the allowed set: err = %v", err)
	}
	if _, err := svc.Create(ctx, f.owner, catalog.AccessFilter{DisabledLibraryIDs: []int{f.tvLibrary}}, Scope{Kind: ScopeSeries, ID: f.series}); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("series in a disabled library: err = %v", err)
	}
	var books int
	if err := f.pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, f.prefix+"books").Scan(&books); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, books) })
	if _, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, f.library(books)); !errors.Is(err, ErrUnsupportedScope) {
		t.Fatalf("audiobook library: err = %v", err)
	}
	if _, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeSeries, ID: f.prefix + "nope"}); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("missing series: err = %v", err)
	}
}

func TestAdvanceAndSkipAreSafeToRetry(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	first := s.CurrentContentID
	advanced, err := svc.Advance(ctx, f.owner, filter, s.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := svc.Advance(ctx, f.owner, filter, s.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	if retried.CurrentContentID != advanced.CurrentContentID || retried.NextContentID != advanced.NextContentID {
		t.Fatalf("retried advance moved on: %+v then %+v", advanced, retried)
	}

	skipped := advanced.NextContentID
	replaced, err := svc.Skip(ctx, f.owner, filter, s.ID, skipped)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.NextContentID == skipped || replaced.CurrentContentID != advanced.CurrentContentID {
		t.Fatalf("skip kept %q: %+v", skipped, replaced)
	}
	again, err := svc.Skip(ctx, f.owner, filter, s.ID, skipped)
	if err != nil {
		t.Fatal(err)
	}
	if again.NextContentID != replaced.NextContentID {
		t.Fatalf("retried skip picked again: %q then %q", replaced.NextContentID, again.NextContentID)
	}
}

func TestShuffleBelongsToItsProfile(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()

	s, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	stranger := Owner{UserID: f.owner.UserID, ProfileID: f.owner.ProfileID + "-other"}
	if _, err := svc.Get(ctx, stranger, catalog.AccessFilter{}, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other profile get: err = %v", err)
	}
	if _, err := svc.Advance(ctx, stranger, catalog.AccessFilter{}, s.ID, s.CurrentContentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other profile advance: err = %v", err)
	}
	if err := svc.Delete(ctx, stranger, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, f.owner, catalog.AccessFilter{}, s.ID); err != nil {
		t.Fatalf("another profile's delete removed the shuffle: %v", err)
	}
	if err := svc.Delete(ctx, f.owner, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, f.owner, catalog.AccessFilter{}, s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted shuffle: err = %v", err)
	}
	if _, err := svc.Get(ctx, f.owner, catalog.AccessFilter{}, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("malformed id: err = %v", err)
	}
}

type fakeCollections struct {
	title string
	items []*models.MediaItem
	err   error
}

func (c fakeCollections) CollectionMembers(context.Context, catalog.CatalogSource, string, catalog.AccessFilter) (string, []*models.MediaItem, error) {
	return c.title, c.items, c.err
}

func TestCollectionShufflePlaysMoviesAndSeriesEpisodes(t *testing.T) {
	f := seed(t)
	ctx := t.Context()
	svc := NewService(f.pool, fakeCollections{title: "Favorites", items: []*models.MediaItem{
		{ContentID: f.movieOK, Type: "movie"},
		{ContentID: f.series, Type: "series"},
	}})

	s, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeUserCollection, ID: "favorites"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Favorites" {
		t.Fatalf("title = %q", s.Title)
	}
	want := append([]string{f.movieOK}, f.allEpisodes()...)
	if got := playthrough(t, svc, f.owner, catalog.AccessFilter{}, s, 5); !sameSet(got, want) {
		t.Fatalf("played %v, want %v", got, want)
	}

	hidden := NewService(f.pool, fakeCollections{err: catalog.ErrCatalogSourceNotFound})
	if _, err := hidden.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeLibraryCollection, ID: "x"}); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("hidden collection: err = %v", err)
	}
	empty := NewService(f.pool, fakeCollections{title: "Empty"})
	if _, err := empty.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeLibraryCollection, ID: "x"}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty collection: err = %v", err)
	}
}

func TestSkippedItemStillPlaysThisCycle(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	skipped := s.NextContentID
	s, err = svc.Skip(ctx, f.owner, filter, s.ID, skipped)
	if err != nil {
		t.Fatal(err)
	}
	if s.NextContentID == skipped {
		t.Fatalf("skip kept %q as next", skipped)
	}
	// The skipped episode never played, so the cycle is not over until it has.
	played := playthrough(t, svc, f.owner, filter, s, 4)
	if !sameSet(played, f.allEpisodes()) {
		t.Fatalf("first cycle %v, want every episode once including the skipped %q", played, skipped)
	}
}

func TestAdvanceReplacesANextItemThatStoppedBeingPlayable(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	gone := s.NextContentID
	if _, err := f.pool.Exec(ctx, `UPDATE media_files SET missing_since = now() WHERE episode_id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	advanced, err := svc.Advance(ctx, f.owner, filter, s.ID, s.CurrentContentID)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.CurrentContentID == gone || advanced.CurrentContentID == s.CurrentContentID {
		t.Fatalf("advance played %q; want a playable episode other than the missing %q and the one that just played", advanced.CurrentContentID, gone)
	}
	if advanced.NextContentID == gone {
		t.Fatalf("missing episode %q picked again as next", gone)
	}
}

func TestGetReplacesANextItemThatStoppedBeingPlayable(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	gone := s.NextContentID
	if _, err := f.pool.Exec(ctx, `UPDATE media_files SET missing_since = now() WHERE episode_id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	read, err := svc.Get(ctx, f.owner, filter, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.NextContentID == gone || read.CurrentContentID != s.CurrentContentID {
		t.Fatalf("read %+v; want the current item kept and the missing %q replaced", read, gone)
	}
}

func TestSkippingTheLastUnplayedItemKeepsIt(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	// Five episodes: after three advances the announced next is the only
	// one this cycle has not played.
	for range 3 {
		if s, err = svc.Advance(ctx, f.owner, filter, s.ID, s.CurrentContentID); err != nil {
			t.Fatal(err)
		}
	}
	last := s.NextContentID
	skipped, err := svc.Skip(ctx, f.owner, filter, s.ID, last)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.NextContentID != last {
		t.Fatalf("skip replaced the last unplayed episode %q with %q, repeating one before it played", last, skipped.NextContentID)
	}
}

// addEpisode adds a playable episode to the fixture's series.
func (f *fixture) addEpisode(t *testing.T, season, number int) string {
	t.Helper()
	id := fmt.Sprintf("%sep-%d-%d", f.prefix, season, number)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, $3, $4, $1)`,
		id, f.series, season, number); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_files (episode_id, content_id, media_folder_id, file_path) VALUES ($1, $2, $3, $4)`,
		id, f.series, f.tvLibrary, "/tv/"+id+".mkv"); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) markMissing(t *testing.T, episodeID string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE media_files SET missing_since = now() WHERE episode_id = $1`, episodeID); err != nil {
		t.Fatal(err)
	}
}

func TestReplacingAGoneItemDoesNotReplayTheOneThatJustFinished(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}
	f.addEpisode(t, 2, 3)

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeason, ID: f.prefix + "season-2"})
	if err != nil {
		t.Fatal(err)
	}
	first := s.CurrentContentID
	if s, err = svc.Advance(ctx, f.owner, filter, s.ID, first); err != nil {
		t.Fatal(err)
	}
	// All three episodes are handed out. The announced one disappears before
	// the one playing now finishes.
	finished, gone := s.CurrentContentID, s.NextContentID
	f.markMissing(t, gone)
	advanced, err := svc.Advance(ctx, f.owner, filter, s.ID, finished)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.CurrentContentID != first {
		t.Fatalf("replacement played %q; want %q, the one episode that neither just finished nor went missing", advanced.CurrentContentID, first)
	}
}

func TestGetReplacesTheOnlyItemOfAOneItemShuffleWhenItGoes(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()
	filter := catalog.AccessFilter{}

	s, err := svc.Create(ctx, f.owner, filter, Scope{Kind: ScopeSeason, ID: f.series + "-S0"})
	if err != nil {
		t.Fatal(err)
	}
	if s.NextContentID != f.special {
		t.Fatalf("one-item shuffle announced %q", s.NextContentID)
	}
	added := f.addEpisode(t, 0, 2)
	f.markMissing(t, f.special)
	read, err := svc.Get(ctx, f.owner, filter, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.NextContentID != added {
		t.Fatalf("read announced %q; want the new special %q", read.NextContentID, added)
	}

	// With nothing left to play the read says so rather than announcing a
	// gone item.
	f.markMissing(t, added)
	if _, err := svc.Get(ctx, f.owner, filter, s.ID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("read with nothing playable: err = %v, want ErrEmpty", err)
	}
}

func TestRepeatedAdvanceChecksAccessFirst(t *testing.T) {
	f := seed(t)
	svc := NewService(f.pool, nil)
	ctx := t.Context()

	s, err := svc.Create(ctx, f.owner, catalog.AccessFilter{}, Scope{Kind: ScopeSeries, ID: f.series})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Advance(ctx, f.owner, catalog.AccessFilter{}, s.ID, s.CurrentContentID); err != nil {
		t.Fatal(err)
	}
	// The same advance again changes nothing, but a viewer who has since
	// lost the series' library gets nothing back.
	lost := catalog.AccessFilter{DisabledLibraryIDs: []int{f.tvLibrary}}
	if _, err := svc.Advance(ctx, f.owner, lost, s.ID, s.CurrentContentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeated advance without access: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Skip(ctx, f.owner, lost, s.ID, "not-the-next-item"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale skip without access: err = %v, want ErrNotFound", err)
	}
}

// A library pick reaches the same items whether it samples or reads the whole
// library: every item the viewer may play, and never one whose file is
// missing or that is over the viewer's limits.
func TestLibraryPicksSampleAndFullReadAgree(t *testing.T) {
	f := seed(t)
	ctx := t.Context()
	limited := catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	cases := []struct {
		name   string
		pool   pool
		filter catalog.AccessFilter
		want   []string
	}{
		{"episodes", pool{episodes: true, libraryID: f.tvLibrary}, catalog.AccessFilter{}, f.allEpisodes()},
		{"limited movies", pool{movies: true, libraryID: f.movies}, limited, []string{f.movieOK}},
	}
	for _, c := range cases {
		for _, draws := range []int{1024, 0} {
			seen := map[string]bool{}
			for range 200 {
				id, err := pickFrom(ctx, f.pool, c.pool, c.filter, "", nil, draws)
				if err != nil {
					t.Fatal(err)
				}
				if id == "" {
					t.Fatalf("%s, draws=%d: no pick from a library with playable items", c.name, draws)
				}
				seen[id] = true
			}
			if got := slices.Collect(maps.Keys(seen)); !sameSet(got, c.want) {
				t.Fatalf("%s, draws=%d picked %v, want exactly %v", c.name, draws, got, c.want)
			}
		}
	}
}

// When no sample finds a playable item, a library pick reads the whole
// library, and only that read reports that nothing is left.
func TestLibraryPickFallsBackWhenSamplesFindNothing(t *testing.T) {
	f := seed(t)
	ctx := t.Context()
	setSampleIDRange(t, `SELECT NULL::bigint AS lo, NULL::bigint AS hi`)
	p := pool{episodes: true, libraryID: f.tvLibrary}

	id, err := pick(ctx, f.pool, p, catalog.AccessFilter{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.allEpisodes(), id) {
		t.Fatalf("pick = %q, want one of %v", id, f.allEpisodes())
	}
	id, err = pick(ctx, f.pool, p, catalog.AccessFilter{}, "", f.allEpisodes())
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("pick with every episode excluded = %q, want none", id)
	}
}

// An episode with several files in the library is no likelier to be sampled
// than one with a single file.
func TestSampledPickCountsEachItemOnce(t *testing.T) {
	f := seed(t)
	ctx := t.Context()
	favored := f.season1[0]
	for i := range 2 {
		if _, err := f.pool.Exec(ctx, `INSERT INTO media_files (episode_id, content_id, media_folder_id, file_path) VALUES ($1, $2, $3, $4)`,
			favored, f.series, f.tvLibrary, fmt.Sprintf("/tv/%s-version-%d.mkv", favored, i)); err != nil {
			t.Fatal(err)
		}
	}
	p := pool{episodes: true, libraryID: f.tvLibrary}
	// One draw per pick, so each pick is the item of a single random file.
	// The library holds five playable episodes, one of them in three files:
	// counted per file it would win 3 picks in 7, counted per item 1 in 5.
	const want = 500
	picks, favoredPicks := 0, 0
	for attempt := 0; picks < want && attempt < 100*want; attempt++ {
		id, err := pickFrom(ctx, f.pool, p, catalog.AccessFilter{}, "", nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			continue
		}
		picks++
		if id == favored {
			favoredPicks++
		}
	}
	if picks < want {
		t.Fatalf("only %d of %d single-draw picks found an item", picks, want)
	}
	if share := float64(favoredPicks) / float64(picks); share > 0.3 {
		t.Fatalf("the episode with three files won %.0f%% of picks, want about 20%%", share*100)
	}
}
