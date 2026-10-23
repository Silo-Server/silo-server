package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

// boxSetFixture is a movie library with box sets on: a collection with three
// movies the viewer sees (Zulu Kappa One, Alpha Kappa Two, Mike Three in
// collection order), one more hidden by the viewer's rating limit and one
// whose lookup is stale, a collection with one local movie, and a standalone
// movie.
type boxSetFixture struct {
	pool      *pgxpool.Pool
	repo      *BoxSetRepository
	prefix    string
	lib       int
	franchise int64
	single    int64
	viewer    AccessFilter
}

func (f boxSetFixture) id(name string) string { return f.prefix + "-" + name }

func (f boxSetFixture) def(sort QuerySort) QueryDefinition {
	return QueryDefinition{MediaScope: playableTypeMovie, LibraryIDs: []int{f.lib}, Sort: sort}
}

// seedBoxSets seeds the fixture. Set SILO_TEST_DATABASE_URL to a migrated
// disposable database; every inserted row is removed on completion.
func seedBoxSets(t *testing.T) boxSetFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UnixNano()
	f := boxSetFixture{pool: pool, repo: NewBoxSetRepository(pool), prefix: fmt.Sprintf("box-set-%d", stamp)}
	f.franchise = stamp%1_000_000_000 + 1_000_000_000
	f.single = f.franchise + 1
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled,box_sets_enabled) VALUES('movies',$1,true,true) RETURNING id`, f.prefix).Scan(&f.lib); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM tmdb_movie_collections WHERE tmdb_collection_id = ANY($1)`, []int64{f.franchise, f.single})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, f.lib)
	})
	// Titles sort against collection order, so an order assertion cannot pass
	// by accident: the collection lists 101, 102, 103, 104, 105.
	movies := []struct {
		id, title, rating string
		year              int
		tmdb, checked     int64
		collection        int64
	}{
		{"second", "Alpha Kappa Two", "PG", 2003, 102, 102, f.franchise},
		{"first", "Zulu Kappa One", "PG", 2001, 101, 101, f.franchise},
		{"third", "Mike Three", "PG", 2005, 103, 103, f.franchise},
		{"hidden", "Bravo Four", "R", 2007, 104, 104, f.franchise},
		{"stale", "Charlie Stale", "PG", 2009, 105, 999, f.franchise},
		{"lonely", "Lonely Kappa Sequel", "PG", 2010, 201, 201, f.single},
		{"solo", "Standalone", "PG", 2012, 301, 301, 0},
	}
	for _, m := range movies {
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age,tmdb_id,year,created_at) VALUES($1,'movie',$2,'matched','{}',$3,$4,$5,$6,'2025-01-01'::timestamptz)`,
			f.id(m.id), m.title, m.rating, map[string]int{"PG": 8, "R": 17}[m.rating], strconv.FormatInt(m.tmdb, 10), m.year)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.id(m.id), f.lib)
		var collection *int64
		if m.collection != 0 {
			collection = new(m.collection)
		}
		exec(`INSERT INTO media_item_tmdb_collections(content_id,checked_tmdb_id,tmdb_collection_id) VALUES($1,$2,$3)`, f.id(m.id), m.checked, collection)
	}
	for _, c := range []TMDBMovieCollection{
		{ID: f.franchise, Name: f.prefix + " Saga Collection", MemberTMDBIDs: []int64{101, 102, 103, 104, 105}},
		{ID: f.single, Name: f.prefix + " Lonely Collection", MemberTMDBIDs: []int64{200, 201}},
	} {
		if err := f.repo.UpsertCollection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	f.viewer = AccessFilter{AllowedLibraryIDs: []int{f.lib}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	return f
}

func TestBoxSetGroupsDB(t *testing.T) {
	f := seedBoxSets(t)
	executor := &QueryExecutor{Pool: f.pool}
	groups, err := executor.boxSetGroups(t.Context(), f.def(QuerySort{}), f.viewer, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].CollectionID != f.franchise {
		t.Fatalf("groups = %+v, want only the franchise", groups)
	}
	if want := []string{f.id("first"), f.id("second"), f.id("third")}; !slices.Equal(groups[0].MemberContentIDs, want) {
		t.Fatalf("members = %v, want visible current members in collection order %v", groups[0].MemberContentIDs, want)
	}
	if groups[0].FirstYear != 2001 || groups[0].LastYear != 2005 {
		t.Fatalf("years = %d-%d, want 2001-2005", groups[0].FirstYear, groups[0].LastYear)
	}
	groups, err = executor.boxSetGroups(t.Context(), f.def(QuerySort{}), f.viewer, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("groups = %+v, want none above the visible member count", groups)
	}
}

func TestBoxSetGroupedBrowseDB(t *testing.T) {
	f := seedBoxSets(t)
	grouped := &QueryExecutor{Pool: f.pool, GroupByWork: true, BoxSetCollections: []int64{f.franchise}}
	// The box set stands at its first member in the requested order.
	for sort, representative := range map[QuerySort]string{
		{Field: "title", Order: "asc"}:  f.id("second"),
		{Field: "title", Order: "desc"}: f.id("first"),
		{Field: "year", Order: "desc"}:  f.id("third"),
	} {
		var got []string
		var after *QueryCursor
		for range 5 {
			page, err := grouped.PreviewCursorPage(t.Context(), f.def(sort), f.viewer, 1, after, true)
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 4 {
				t.Fatalf("%v: total = %d, want 4 (box set, stale, lonely, solo)", sort, page.Total)
			}
			for _, item := range page.Items {
				got = append(got, item.ContentID)
			}
			if !page.HasMore {
				break
			}
			after = page.Next
		}
		members := 0
		for _, id := range got {
			if slices.Contains([]string{f.id("first"), f.id("second"), f.id("third")}, id) {
				members++
			}
		}
		if len(got) != 4 || members != 1 || !slices.Contains(got, representative) {
			t.Fatalf("%v: rows = %v, want 4 rows with only %s for the box set", sort, got, representative)
		}
	}
	index, err := grouped.LetterIndex(t.Context(), f.def(QuerySort{Field: "title", Order: "asc"}), f.viewer)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, letter := range index.Letters {
		total += letter.Count
	}
	if total != 4 {
		t.Fatalf("letter index counts %d titles, want the 4 grouped rows: %+v", total, index.Letters)
	}
	// A name prefix lists the box set only under its representative's letter,
	// where the letter index files it: "Alpha Kappa Two" leads it by title, so
	// "Zulu Kappa One" does not bring it under Z.
	for prefix, want := range map[string][]string{"a": {f.id("second")}, "z": nil} {
		viewer := f.viewer
		viewer.NamePrefix = prefix
		page, err := grouped.PreviewCursorPage(t.Context(), f.def(QuerySort{Field: "title", Order: "asc"}), viewer, 10, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ContentID)
		}
		if !slices.Equal(got, want) || page.Total != len(want) {
			t.Fatalf("prefix %q: rows = %v (total %d), want %v", prefix, got, page.Total, want)
		}
	}
}

func TestBoxSetSearchAndSyncDB(t *testing.T) {
	f := seedBoxSets(t)
	ctx := t.Context()
	executor := &QueryExecutor{Pool: f.pool}
	search := func(query string, libraryIDs []int, viewer AccessFilter, limit, offset int) []int64 {
		t.Helper()
		ids, err := executor.searchBoxSetCollections(ctx, query, libraryIDs, viewer, limit, offset)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if byName := search(f.prefix+" saga", []int{f.lib}, f.viewer, 10, 0); !slices.Equal(byName, []int64{f.franchise}) {
		t.Fatalf("name search = %v, want [%d]", byName, f.franchise)
	}
	if next := search(f.prefix+" saga", []int{f.lib}, f.viewer, 10, 1); len(next) != 0 {
		t.Fatalf("second page = %v, want empty", next)
	}
	// A library without the collection's movies finds nothing, so matches
	// elsewhere cannot use up a library's candidates.
	everywhere := AccessFilter{}
	if other := search(f.prefix+" saga", []int{f.lib + 1_000_000}, everywhere, 10, 0); len(other) != 0 {
		t.Fatalf("search in another library = %v, want empty", other)
	}
	// "kappa" is in two of the franchise's titles but one of the single
	// collection's, and in neither name.
	if byTitle := search("kappa", []int{f.lib}, f.viewer, 50, 0); !slices.Contains(byTitle, f.franchise) || slices.Contains(byTitle, f.single) {
		t.Fatalf("title search = %v, want the franchise and not the single", byTitle)
	}
	// Titles in another library do not make a match.
	if elsewhere := search("kappa", []int{f.lib + 1_000_000}, everywhere, 50, 0); slices.Contains(elsewhere, f.franchise) {
		t.Fatalf("title search in another library = %v, want no franchise", elsewhere)
	}
	// Only titles the viewer can see count: "bravo" is in a visible title and
	// in the R-rated one, so the PG viewer must not learn the hidden title
	// matches through the box set.
	if _, err := f.pool.Exec(ctx, `UPDATE media_items SET title = 'Mike Bravo Three' WHERE content_id = $1`, f.id("third")); err != nil {
		t.Fatal(err)
	}
	if all := search("bravo", []int{f.lib}, everywhere, 50, 0); !slices.Contains(all, f.franchise) {
		t.Fatalf("unrestricted title search = %v, want the franchise", all)
	}
	if restricted := search("bravo", []int{f.lib}, f.viewer, 50, 0); slices.Contains(restricted, f.franchise) {
		t.Fatalf("PG title search = %v, want no franchise from a hidden title", restricted)
	}

	// A collection no movie belongs to any more is deleted; one with movies
	// is kept.
	if _, err := f.pool.Exec(ctx, `DELETE FROM media_item_tmdb_collections WHERE content_id = $1`, f.id("lonely")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.DeleteUnusedCollections(ctx); err != nil {
		t.Fatal(err)
	}
	if kept, err := f.repo.Collections(ctx, []int64{f.franchise, f.single}); err != nil || len(kept) != 1 || kept[f.franchise].ID != f.franchise {
		t.Fatalf("collections after deleting unused = %v, %v; want only the franchise", kept, err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM media_item_tmdb_collections WHERE content_id = $1`, f.id("solo")); err != nil {
		t.Fatal(err)
	}
	// An ID not in plain form could never be recorded as looked up, so it is
	// never due rather than due on every pass.
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres,tmdb_id,created_at) VALUES($1,'movie','Zero Padded','matched','{}','0603',now())`, f.id("padded")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.id("padded"), f.lib); err != nil {
		t.Fatal(err)
	}
	due, err := f.repo.MoviesNeedingCollectionLookup(ctx, time.Now().Add(-time.Hour), 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, m := range due {
		if strings.HasPrefix(m.ContentID, f.prefix) {
			mine = append(mine, m.ContentID)
		}
	}
	slices.Sort(mine)
	if want := []string{f.id("lonely"), f.id("solo"), f.id("stale")}; !slices.Equal(mine, want) {
		t.Fatalf("due = %v, want the never looked up and the rematched movies %v", mine, want)
	}
	// A movie this run already tried is skipped.
	due, err = f.repo.MoviesNeedingCollectionLookup(ctx, time.Now().Add(-time.Hour), 1000, []string{f.id("solo")})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range due {
		if m.ContentID == f.id("solo") {
			t.Fatalf("due = %v, want the skipped movie left out", due)
		}
	}
}

func TestBoxSetCollectionArtworkDB(t *testing.T) {
	f := seedBoxSets(t)
	ctx := t.Context()
	const source, replaced = "https://image.tmdb.org/t/p/original/a.jpg", "https://image.tmdb.org/t/p/original/b.jpg"
	cachedKey := "tmdb/collections/" + strconv.FormatInt(f.franchise, 10) + "/poster/original.rev1.webp"
	upsert := func(poster string) {
		t.Helper()
		if err := f.repo.UpsertCollection(ctx, TMDBMovieCollection{ID: f.franchise, Name: "Saga", PosterSourcePath: poster}); err != nil {
			t.Fatal(err)
		}
	}
	posterOf := func() (string, string) {
		t.Helper()
		c, err := f.repo.Collections(ctx, []int64{f.franchise})
		if err != nil {
			t.Fatal(err)
		}
		return c[f.franchise].PosterPath, c[f.franchise].PosterThumbhash
	}
	isDue := func() bool {
		t.Helper()
		due, err := f.repo.CollectionArtworkNeedingCache(ctx, 10_000, nil)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(due, func(d CollectionArtworkDue) bool {
			return d.CollectionID == f.franchise && d.Slot == BoxSetPosterSlot && d.SourcePath == source
		})
	}

	upsert(source)
	if path, _ := posterOf(); path != source || !isDue() {
		t.Fatalf("a new poster is served from TMDB and due for caching; path = %q", path)
	}
	if err := f.repo.SetCollectionArtwork(ctx, f.franchise, BoxSetPosterSlot, source, cachedKey, "hash"); err != nil {
		t.Fatal(err)
	}
	upsert(source)
	if path, hash := posterOf(); path != cachedKey || hash != "hash" || isDue() {
		t.Fatalf("a refetch with the same poster keeps the cached copy; path = %q hash = %q", path, hash)
	}
	upsert(replaced)
	if path, hash := posterOf(); path != replaced || hash != "" {
		t.Fatalf("a new poster replaces the cached copy until it is cached; path = %q hash = %q", path, hash)
	}
	// A cache of the old source finishing late must not overwrite the new one.
	if err := f.repo.SetCollectionArtwork(ctx, f.franchise, BoxSetPosterSlot, source, cachedKey, "hash"); err != nil {
		t.Fatal(err)
	}
	if path, _ := posterOf(); path != replaced {
		t.Fatalf("stale cache result applied: path = %q", path)
	}
	var queued bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM artwork_revision_gc_candidates WHERE original_path = $1)`, cachedKey).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the displaced cached poster was not queued for collection")
	}
	_, _ = f.pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, cachedKey)
}
