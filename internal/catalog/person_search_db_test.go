package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPersonSearchScopeAndRankingPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-search-" + uuid.NewString()
	name := "Nathan " + prefix
	names := []string{name, "Alice, " + name, "Bob, " + name, name + " Jr", "Zoe, " + name}
	baseID := time.Now().UnixNano()
	ids := make([]int64, len(names))
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
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	for i, personName := range names {
		ids[i] = baseID + int64(i)
		exec(`INSERT INTO people(id, name) VALUES ($1, $2)`, ids[i], personName)
	}
	for i, credit := range []struct {
		person   int
		typeName string
		kind     int
	}{
		{0, "movie", 1}, {0, "series", 1}, {0, "audiobook", 8},
		{1, "audiobook", 7}, {2, "movie", 2}, {3, "series", 1},
	} {
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		exec(`INSERT INTO media_items(content_id, type, title) VALUES ($1, $2, 'Synthetic title')`, contentID, credit.typeName)
		exec(`INSERT INTO item_people(id, content_id, person_id, kind) VALUES ($1, $2, $3, $4)`, baseID+int64(i), contentID, ids[credit.person], credit.kind)
	}
	repo := NewPersonRepository(pool)
	for _, tc := range []struct {
		name, scope string
		limit       int
		want        []int64
	}{
		{"all exact before limit", "", 1, ids[:1]},
		{"all retains credited matches", "", 20, ids[:4]},
		{"media excludes audiobook-only credits", "video", 20, []int64{ids[0], ids[2], ids[3]}},
		{"media exact before limit", "video", 1, ids[:1]},
		{"audiobooks include narrators and authors", "audiobook", 20, ids[:2]},
		{"movies include directors", "movie", 20, []int64{ids[0], ids[2]}},
		{"series", "series", 20, []int64{ids[0], ids[3]}},
		{"empty scope results", "ebook", 20, []int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people, err := repo.SearchScoped(t.Context(), "  "+strings.ToLower(name)+"  ", tc.limit, tc.scope, AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, len(people))
			for i, person := range people {
				got[i] = person.ID
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	legacy, err := repo.SearchAlphabetical(ctx, name, 1, AccessFilter{})
	if err != nil || len(legacy) != 1 || legacy[0].ID != ids[1] {
		t.Fatalf("legacy alphabetical search changed: %+v, %v", legacy, err)
	}
	legacy, err = repo.SearchAlphabetical(ctx, name, 20, AccessFilter{})
	if err != nil || len(legacy) != 4 || slices.ContainsFunc(legacy, func(p models.Person) bool { return p.ID == ids[4] }) {
		t.Fatalf("legacy search listed a person with no visible credit: %+v, %v", legacy, err)
	}
}

func TestPersonSearchViewerAccessPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-access-" + uuid.NewString()
	baseID := time.Now().UnixNano()
	ids := make([]int64, 8)
	libraries := make([]int, 2)
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
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	})
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range ids {
		ids[i] = baseID + int64(i)
		name := prefix
		if i > 0 {
			name = fmt.Sprintf("%c %s", 'A'+i-1, prefix)
		}
		exec(`INSERT INTO people(id, name) VALUES ($1, $2)`, ids[i], name)
		if i == 6 {
			continue
		} // No credited items.
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		itemType, rating := "movie", "G"
		if i == 3 {
			rating = "R"
		}
		if i == 4 {
			itemType = "ebook"
		}
		if i == 7 {
			itemType = "series"
		}
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,$2,'Synthetic title',$3,$4)`, contentID, itemType, rating, access.StoredRating(rating))
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$1,1)`, ids[i], contentID)
		if i != 5 { // Orphan item has no library membership.
			library := libraries[0]
			if i == 0 {
				library = libraries[1]
			}
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentID, library)
		}
		if i == 2 {
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentID, libraries[1])
		}
	}
	repo := NewPersonRepository(pool)
	for _, tc := range []struct {
		name, scope string
		limit       int
		filter      AccessFilter
		want        []int64
	}{
		{"restricted library", "movie", 20, AccessFilter{AllowedLibraryIDs: libraries[:1]}, []int64{ids[1], ids[2], ids[3]}},
		{"access before ranking and limit", "movie", 1, AccessFilter{AllowedLibraryIDs: libraries[:1]}, []int64{ids[1]}},
		{"no allowed libraries", "", 20, AccessFilter{AllowedLibraryIDs: []int{}}, nil},
		{"disabled membership hides shared and orphan items", "movie", 20, AccessFilter{DisabledLibraryIDs: libraries[1:]}, []int64{ids[1], ids[3]}},
		{"rating ceiling", "movie", 20, AccessFilter{AllowedLibraryIDs: libraries[:1], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, []int64{ids[1], ids[2]}},
		{"excluded type across all scopes", "", 20, AccessFilter{AllowedLibraryIDs: libraries[:1], ExcludedMediaTypes: []string{"ebook"}}, []int64{ids[1], ids[2], ids[3], ids[7]}},
		{"combined restrictions across all scopes", "", 20, AccessFilter{AllowedLibraryIDs: libraries[:1], DisabledLibraryIDs: libraries[1:], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}, ExcludedMediaTypes: []string{"ebook"}}, []int64{ids[1], ids[7]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people, err := repo.SearchScoped(t.Context(), prefix, tc.limit, tc.scope, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, len(people))
			for i, p := range people {
				got[i] = p.ID
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			requireGetVisibleMatchesSearch(t, repo, prefix, ids, tc.filter)
		})
	}
}

// requireGetVisibleMatchesSearch checks that person detail admits exactly the
// people an unscoped people search returns under the same viewer access, and
// that a hidden person reads like an unknown ID.
func requireGetVisibleMatchesSearch(t *testing.T, repo *PersonRepository, query string, ids []int64, filter AccessFilter) {
	t.Helper()
	searched, err := repo.SearchScoped(t.Context(), query, 100, "", filter)
	if err != nil {
		t.Fatal(err)
	}
	listed := make(map[int64]bool, len(searched))
	for _, p := range searched {
		listed[p.ID] = true
	}
	// The v1 bridge search lists the same people, in its own order.
	legacy, err := repo.SearchAlphabetical(t.Context(), query, 100, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != len(searched) || slices.ContainsFunc(legacy, func(p models.Person) bool { return !listed[p.ID] }) {
		t.Fatalf("v1 search listed %+v, v2 search %+v", legacy, searched)
	}
	for _, id := range ids {
		person, err := repo.GetVisible(t.Context(), id, filter)
		switch {
		case listed[id] && (err != nil || person == nil || person.ID != id):
			t.Fatalf("person %d is searchable but detail answered %+v, %v", id, person, err)
		case !listed[id] && !errors.Is(err, pgx.ErrNoRows):
			t.Fatalf("person %d is not searchable but detail answered %+v, %v", id, person, err)
		}
	}
	if _, err := repo.GetVisible(t.Context(), -1, filter); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown person: %v", err)
	}
}

func TestPersonSearchEpisodeParentAccessPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-episode-" + uuid.NewString()
	baseID := time.Now().UnixNano()
	ids := make([]int64, 6)
	libraries := make([]int, 2)
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
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	})
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('tv',$1,true) RETURNING id`, prefix).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range ids {
		ids[i] = baseID + int64(i)
		name := prefix
		if i > 0 {
			name = fmt.Sprintf("%c %s", 'A'+i-1, prefix)
		}
		exec(`INSERT INTO people(id,name) VALUES($1,$2)`, ids[i], name)
		episodeID := fmt.Sprintf("%s-episode-%d", prefix, i)
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,'episode','Synthetic episode','G',0)`, episodeID)
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$1,1)`, ids[i], episodeID)
		if i != 1 { // Accessible episode has no independent library membership.
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, episodeID, libraries[0])
		}
		if i == 5 { // An episode without a parent cannot establish visibility.
			continue
		}
		seriesID := fmt.Sprintf("%s-series-%d", prefix, i)
		rating := "G"
		if i == 3 {
			rating = "R"
		}
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,'series','Synthetic series',$2,$3)`, seriesID, rating, access.StoredRating(rating))
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,'Synthetic episode')`, episodeID, seriesID)
		if i != 4 { // Orphan parent, despite the child's permissive membership.
			library := libraries[0]
			if i == 0 {
				library = libraries[1]
			}
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, seriesID, library)
		}
		if i == 2 { // A disabled membership hides a series in both libraries.
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, seriesID, libraries[1])
		}
	}
	repo := NewPersonRepository(pool)
	for _, scope := range []string{"", "episode"} {
		for _, tc := range []struct {
			name   string
			filter AccessFilter
			limit  int
			want   []int64
		}{
			{"allowed library", AccessFilter{AllowedLibraryIDs: libraries[:1]}, 20, []int64{ids[1], ids[2], ids[3]}},
			{"disabled library", AccessFilter{DisabledLibraryIDs: libraries[1:]}, 20, []int64{ids[1], ids[3]}},
			{"parent rating", AccessFilter{AllowedLibraryIDs: libraries[:1], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, 20, []int64{ids[1], ids[2]}},
			{"access before limit", AccessFilter{AllowedLibraryIDs: libraries[:1], DisabledLibraryIDs: libraries[1:], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, 1, []int64{ids[1]}},
			{"excluded credit type", AccessFilter{ExcludedMediaTypes: []string{"episode"}}, 20, nil},
			{"missing parent", AccessFilter{}, 20, ids[:5]},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				people, err := repo.SearchScoped(t.Context(), prefix, tc.limit, scope, tc.filter)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]int64, len(people))
				for i, person := range people {
					got[i] = person.ID
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				if scope == "" {
					requireGetVisibleMatchesSearch(t, repo, prefix, ids, tc.filter)
				}
			})
		}
	}
}

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

func TestPersonSearchMatchesWordStartsPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-words-" + uuid.NewString()
	word := "hacks" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	names := []string{
		word,
		"Lark " + word + "haw",
		"Chad T" + word + "ton",
		"Jean-" + word,
		"O'" + word,
		word + " Smithers",
		"A " + word + " Smithers",
		"Erik GROẞ" + strings.ToUpper(word[5:]),
		"Jane  Doe" + word[5:],
		"A Jane Doe" + word[5:],
	}
	baseID := time.Now().UnixNano()
	ids := make([]int64, len(names))
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
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	for i, name := range names {
		ids[i] = baseID + int64(i)
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		exec(`INSERT INTO people(id, name) VALUES ($1, $2)`, ids[i], name)
		exec(`INSERT INTO media_items(content_id, type, title) VALUES ($1, 'movie', 'Synthetic title')`, contentID)
		exec(`INSERT INTO item_people(id, content_id, person_id, kind) VALUES ($1, $2, $1, 1)`, ids[i], contentID)
	}
	repo := NewPersonRepository(pool)
	for _, tc := range []struct {
		name, query string
		want        []int64 // want[0] must rank first; the rest in any order.
	}{
		{"word starts only, exact first", strings.ToUpper(word), []int64{ids[0], ids[1], ids[3], ids[4], ids[5], ids[6]}},
		{"partial last word", word + " smi", []int64{ids[6], ids[5]}},
		{"words in any order", "smithers " + word, []int64{ids[6], ids[5]}},
		{"exact name ignores extra spaces", word + "  smithers", []int64{ids[5], ids[6]}},
		{"repeated words", word + " " + strings.ToUpper(word), []int64{ids[6], ids[0], ids[1], ids[3], ids[4], ids[5]}},
		{"leading apostrophe", "'" + word, []int64{ids[4]}},
		{"leading hyphen", "-" + word, []int64{ids[3]}},
		{"database case folding", "groß" + word[5:], []int64{ids[7]}},
		{"exact name keeps its own spacing", "jane  doe" + word[5:], []int64{ids[8], ids[9]}},
		{"mid-word fragment", word[1:], nil},
		{"wildcards are literal", "%" + word[1:], nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people, err := repo.SearchScoped(t.Context(), tc.query, 20, "", AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, len(people))
			for i, p := range people {
				got[i] = p.ID
			}
			if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, id := range tc.want {
				if !slices.Contains(got, id) {
					t.Fatalf("got %v, missing %d", got, id)
				}
			}
		})
	}
	// An empty query lists people and must bind no unread parameter.
	for _, scope := range []string{"", "movie"} {
		if people, err := repo.SearchScoped(ctx, "  ", 1, scope, AccessFilter{}); err != nil || len(people) != 1 {
			t.Fatalf("empty query in scope %q: %+v, %v", scope, people, err)
		}
	}
	if people, err := repo.SearchAlphabetical(ctx, "", 1, AccessFilter{}); err != nil || len(people) != 1 {
		t.Fatalf("empty v1 query: %+v, %v", people, err)
	}
}
