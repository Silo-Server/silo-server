package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestJellyfinPersonReadsMatchNativeVisibilityPostgres pins the Jellyfin
// person reads (SearchVisibleWithOptions, SearchVisible, EnsureAccessible) to
// the native person-detail rule (GetVisible) on one fixture: an episode credit
// counts when the viewer can see the parent series, and hidden-library,
// rating-limited and disabled-library people stay hidden. The Jellyfin reads
// count only video credits, so an ebook-only author is the one person native
// detail shows and they do not.
func TestJellyfinPersonReadsMatchNativeVisibilityPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-jf-" + uuid.NewString()
	baseID := time.Now().UnixNano()
	const (
		movieCast = iota
		seriesCast
		guestStar
		hiddenLibraryOnly
		aboveLimitOnly
		matureGuestStar
		visibleAndHidden
		ebookAuthor
		orphanGuestStar
		peopleCount
	)
	ids := make([]int64, peopleCount)
	var movies, tv, hidden int
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM item_people WHERE person_id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM episodes WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{movies, tv, hidden})
	})
	for _, library := range []struct {
		kind string
		id   *int
	}{{"movies", &movies}, {"tv", &tv}, {"movies", &hidden}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES($1,$2,true) RETURNING id`, library.kind, prefix).Scan(library.id); err != nil {
			t.Fatal(err)
		}
	}
	item := func(suffix, itemType, rating string, libraries ...int) string {
		t.Helper()
		contentID := prefix + "-" + suffix
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,$2,'Synthetic title',$3,$4)`, contentID, itemType, rating, access.StoredRating(rating))
		for _, library := range libraries {
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentID, library)
		}
		return contentID
	}
	episode := func(suffix, seriesID string, libraries ...int) string {
		t.Helper()
		contentID := item(suffix, "episode", "", libraries...)
		if seriesID != "" {
			exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,'Synthetic episode')`, contentID, seriesID)
		}
		return contentID
	}
	movie := item("movie", "movie", "PG", movies)
	matureMovie := item("mature-movie", "movie", "R", movies)
	hiddenMovie := item("hidden-movie", "movie", "PG", hidden)
	series := item("series", "series", "TV-PG", tv)
	matureSeries := item("mature-series", "series", "TV-MA", tv)
	// The guest star's episode carries its own movies-library membership, which
	// must not count: an episode credit belongs to its series' libraries.
	guestEpisode := episode("episode", series, movies)
	matureEpisode := episode("mature-episode", matureSeries)
	orphanEpisode := episode("orphan-episode", "", tv)
	ebook := item("ebook", "ebook", "", movies)
	credits := map[int][]string{
		movieCast:         {movie},
		seriesCast:        {series},
		guestStar:         {guestEpisode},
		hiddenLibraryOnly: {hiddenMovie},
		aboveLimitOnly:    {matureMovie},
		matureGuestStar:   {matureEpisode},
		visibleAndHidden:  {movie, hiddenMovie},
		ebookAuthor:       {ebook},
		orphanGuestStar:   {orphanEpisode},
	}
	creditID := baseID
	for i := range ids {
		ids[i] = baseID + int64(i)
		exec(`INSERT INTO people(id,name) VALUES($1,$2)`, ids[i], fmt.Sprintf("%c %s", 'A'+i, prefix))
		for _, contentID := range credits[i] {
			exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$3,1)`, creditID, contentID, ids[i])
			creditID++
		}
	}
	people := func(indexes ...int) []int64 {
		out := []int64{}
		for _, i := range indexes {
			out = append(out, ids[i])
		}
		return out
	}

	repo := NewPersonRepository(pool)
	search := func(opts PersonSearchOptions) []int64 {
		t.Helper()
		opts.Term, opts.Limit, opts.IncludeTotal = prefix, 100, true
		found, total, err := repo.SearchVisibleWithOptions(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]int64, len(found))
		for i, p := range found {
			got[i] = p.ID
		}
		if total != len(got) {
			t.Fatalf("%+v: total %d for %d people", opts, total, len(got))
		}
		return got
	}
	ceiling := access.MaturityLimits{MaxContentRating: "PG-13"}
	for _, tc := range []struct {
		name   string
		filter AccessFilter
		want   []int64
	}{
		{"admin", AccessFilter{}, people(movieCast, seriesCast, guestStar, hiddenLibraryOnly, aboveLimitOnly, matureGuestStar, visibleAndHidden)},
		{"allowed libraries", AccessFilter{AllowedLibraryIDs: []int{movies, tv}}, people(movieCast, seriesCast, guestStar, aboveLimitOnly, matureGuestStar, visibleAndHidden)},
		{"disabled library", AccessFilter{DisabledLibraryIDs: []int{hidden}}, people(movieCast, seriesCast, guestStar, aboveLimitOnly, matureGuestStar, visibleAndHidden)},
		{"rating limit", AccessFilter{AllowedLibraryIDs: []int{movies, tv}, MaturityLimits: ceiling}, people(movieCast, seriesCast, guestStar, visibleAndHidden)},
		{"no libraries", AccessFilter{AllowedLibraryIDs: []int{}}, people()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := search(PersonSearchOptions{Filter: tc.filter}); !slices.Equal(got, tc.want) {
				t.Fatalf("Jellyfin people = %v, want %v", got, tc.want)
			}
			listed := make(map[int64]bool, len(tc.want))
			for _, id := range tc.want {
				listed[id] = true
			}
			for i, id := range ids {
				native, nativeErr := repo.GetVisible(ctx, id, tc.filter)
				if nativeErr != nil && !errors.Is(nativeErr, pgx.ErrNoRows) {
					t.Fatal(nativeErr)
				}
				// Only the ebook author's credit lies outside the Jellyfin reads.
				if nativeVisible := nativeErr == nil && native.ID == id; nativeVisible != listed[id] && i != ebookAuthor {
					t.Fatalf("person %d: native detail visible=%v, Jellyfin people listed=%v", i, nativeVisible, listed[id])
				}
				err := repo.EnsureAccessible(ctx, id, tc.filter)
				if (err == nil) != listed[id] || err != nil && !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("person %d: EnsureAccessible = %v, listed %v", i, err, listed[id])
				}
				byName, _, err := repo.SearchVisible(ctx, fmt.Sprintf("%c %s", 'A'+i, prefix), true, 1, 0, tc.filter, false)
				if err != nil || (len(byName) == 1) != listed[id] {
					t.Fatalf("person %d: exact name lookup %+v, %v; listed %v", i, byName, err, listed[id])
				}
			}
			if _, err := repo.GetVisible(ctx, ids[ebookAuthor], tc.filter); tc.name == "admin" && err != nil {
				t.Fatalf("native detail hides the ebook author from an admin: %v", err)
			}
		})
	}

	restricted := AccessFilter{AllowedLibraryIDs: []int{movies, tv}, MaturityLimits: ceiling}
	for _, tc := range []struct {
		name string
		opts PersonSearchOptions
		want []int64
	}{
		{"tv library holds series cast and guest stars", PersonSearchOptions{LibraryID: tv}, people(seriesCast, guestStar, matureGuestStar)},
		{"movies library ignores the episode's own membership", PersonSearchOptions{LibraryID: movies}, people(movieCast, aboveLimitOnly, visibleAndHidden)},
		{"series holds its guest stars", PersonSearchOptions{ContentID: series}, people(seriesCast, guestStar)},
		{"episode holds its own credits", PersonSearchOptions{ContentID: guestEpisode}, people(guestStar)},
		{"movie", PersonSearchOptions{ContentID: movie}, people(movieCast, visibleAndHidden)},
		{"hidden movie for admin", PersonSearchOptions{ContentID: hiddenMovie}, people(hiddenLibraryOnly, visibleAndHidden)},
		{"rating limit within the tv library", PersonSearchOptions{LibraryID: tv, Filter: restricted}, people(seriesCast, guestStar)},
		{"rating limit on a mature series", PersonSearchOptions{ContentID: matureSeries, Filter: restricted}, people()},
		{"hidden library under an allowlist", PersonSearchOptions{LibraryID: hidden, Filter: restricted}, people()},
		{"hidden movie under an allowlist", PersonSearchOptions{ContentID: hiddenMovie, Filter: restricted}, people()},
		{"name bound", PersonSearchOptions{NameStartsWith: "c", Filter: restricted}, people(guestStar)},
		{"name range", PersonSearchOptions{NameStartsWithOrGreater: "b", NameLessThan: "d", Filter: restricted}, people(seriesCast, guestStar)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := search(tc.opts); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("paging keeps the visible total", func(t *testing.T) {
		page, total, err := repo.SearchVisibleWithOptions(ctx, PersonSearchOptions{Term: prefix, Limit: 2, Offset: 1, Filter: restricted, IncludeTotal: true})
		if err != nil {
			t.Fatal(err)
		}
		if total != 4 || len(page) != 2 || page[0].ID != ids[seriesCast] || page[1].ID != ids[guestStar] {
			t.Fatalf("page %+v, total %d", page, total)
		}
	})
}

// TestPersonSearchRanksExactNamesFirstWithCustomPlansPostgres pins v2 people
// search to exact names first, then name and ID order, with viewer access
// applied in both groups before the limit, and the v1 bridge search to plain
// name order. It also pins every people search to a plan for its own words:
// no named statement is left for PostgreSQL to switch to a generic plan, which
// checks the credits of every matching person before sorting them.
func TestPersonSearchRanksExactNamesFirstWithCustomPlansPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := t.Context()
	token := "personrank" + strings.ReplaceAll(uuid.NewString(), "-", "")
	const (
		exactVisible = iota
		exactHidden
		firstHidden
		alice
		bob
	)
	ids, visible := seedPersonSearchPeople(t, pool, token, []string{
		exactVisible: strings.ToUpper(token),
		exactHidden:  token,
		firstHidden:  "0 " + token,
		alice:        "A " + token,
		bob:          "B " + token,
	}, exactHidden, firstHidden)
	// A generic plan must still answer correctly; the statement check below
	// shows that the server never asks for one.
	if _, err := pool.Exec(ctx, "SET plan_cache_mode = force_generic_plan"); err != nil {
		t.Fatal(err)
	}

	repo := NewPersonRepository(pool)
	filter := AccessFilter{AllowedLibraryIDs: []int{visible}}
	for _, tc := range []struct {
		name   string
		search func() ([]models.Person, error)
		want   []int
	}{
		{"scoped, limit 2", func() ([]models.Person, error) {
			return repo.SearchScoped(ctx, " "+token+" ", 2, MediaScopeVideo, filter)
		}, []int{exactVisible, alice}},
		{"scoped", func() ([]models.Person, error) { return repo.SearchScoped(ctx, token, 20, "", filter) }, []int{exactVisible, alice, bob}},
		{"v1 bridge", func() ([]models.Person, error) { return repo.SearchAlphabetical(ctx, token, 20, filter) }, []int{alice, bob, exactVisible}},
	} {
		want := make([]int64, len(tc.want))
		for i, index := range tc.want {
			want[i] = ids[index]
		}
		// Past PostgreSQL's five custom plans for a named statement.
		for range 7 {
			people, err := tc.search()
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, len(people))
			for i, p := range people {
				got[i] = p.ID
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s: got %v, want %v", tc.name, got, want)
			}
		}
	}
	var named int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_prepared_statements
		WHERE statement LIKE '%FROM people WHERE%' AND statement NOT LIKE '%pg_prepared_statements%'`).Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != 0 {
		t.Fatalf("people search retained %d named statements eligible for generic plans", named)
	}
}

// seedPersonSearchPeople inserts one person per name, each credited on a movie
// in a visible library, or in a second, hidden one for the hidden indexes, and
// removes them all when the test ends. It returns the people's IDs in name
// order and the visible library.
func seedPersonSearchPeople(t *testing.T, pool *pgxpool.Pool, token string, names []string, hiddenIndexes ...int) ([]int64, int) {
	t.Helper()
	ctx := t.Context()
	ids := make([]int64, len(names))
	var visible, hidden int
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM item_people WHERE person_id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, token+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{visible, hidden})
	})
	for i, library := range []*int{&visible, &hidden} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, token).Scan(library); err != nil {
			t.Fatal(err)
		}
		contentID := fmt.Sprintf("%s-%d", token, i)
		exec(`INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Synthetic title')`, contentID)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentID, *library)
	}
	baseID := time.Now().UnixNano()
	for i, name := range names {
		ids[i] = baseID + int64(i)
		exec(`INSERT INTO people(id,name) VALUES($1,$2)`, ids[i], name)
		credited := token + "-0"
		if slices.Contains(hiddenIndexes, i) {
			credited = token + "-1"
		}
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$1,1)`, ids[i], credited)
	}
	return ids, visible
}
