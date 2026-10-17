package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Search returns what a viewer typing the query is looking for, on the cursor
// and offset paths alike. A typo match must contain every word of the query,
// and a title starting with the query ranks above one that merely contains it.
func TestSearchRelevancePostgres(t *testing.T) {
	pool := searchOptimizationPool(t, nil)
	ctx := t.Context()
	library, id := seedSearchRelevanceFixture(t, pool)
	places := []string{"good-place", "hiding-place", "dark-place", "secret-place", "palace", "place-in-the-sun"}

	repo := NewItemRepository(pool)
	filter := AccessFilter{AllowedLibraryIDs: []int{library}}
	search := func(t *testing.T, query string) []string {
		t.Helper()
		page, err := repo.SearchCursorPage(ctx, query, nil, 50, nil, filter, true)
		if err != nil {
			t.Fatal(err)
		}
		items, _, err := repo.Search(ctx, query, nil, 50, 0, filter)
		if err != nil {
			t.Fatal(err)
		}
		cursor, offset := contentIDsFromMediaItems(page.Items), contentIDsFromMediaItems(items)
		if !slices.Equal(cursor, offset) {
			t.Fatalf("cursor results %v differ from offset results %v", cursor, offset)
		}
		return cursor
	}
	ids := func(names ...string) []string {
		out := make([]string, len(names))
		for i, name := range names {
			out[i] = id(name)
		}
		return out
	}

	t.Run("typo matches need every word", func(t *testing.T) {
		// No title contains "godd". The Good Place matches every word within
		// one typo each; each "The ... Place" title lacks "god".
		if got := search(t, "the godd place"); !slices.Equal(got, ids("good-place", "mystic")) &&
			!slices.Equal(got, ids("mystic", "good-place")) {
			t.Fatalf("results = %v, want only The Good Place and the alias matching every word", got)
		}
	})
	t.Run("typo matches beside exact hits need every word", func(t *testing.T) {
		got := search(t, "the god place")
		if !slices.Equal(got, ids("mystic", "good-place")) {
			t.Fatalf("results = %v, want the alias hit, then The Good Place", got)
		}
	})
	t.Run("typo matches check the alias with every word", func(t *testing.T) {
		got := search(t, "lantrn harbor")
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(ids("lantern-harbor", "harbor-tales")))) {
			t.Fatalf("results = %v, want Lantern Harbor and the item whose long alias has every word", got)
		}
	})
	t.Run("relaxes only when no title has every word", func(t *testing.T) {
		// "hidden" is three edits from "hiding", so no title has every word;
		// the titles matching "the" and "place" remain.
		got := search(t, "the hidden place")
		if !slices.Contains(got, id("hiding-place")) {
			t.Fatalf("relaxed results = %v, want The Hiding Place among them", got)
		}
		for _, contentID := range got {
			if !slices.Contains(ids(places...), contentID) && contentID != id("mystic") {
				t.Fatalf("relaxed results = %v, include %s, which lacks \"place\"", got, contentID)
			}
		}
	})
	t.Run("leading titles rank above containing titles", func(t *testing.T) {
		got := search(t, "the godfather")
		if len(got) != 6 || got[0] != id("godfather") ||
			!slices.Equal(slices.Sorted(slices.Values(got[1:4])), slices.Sorted(slices.Values(ids("godfather-2", "godfather-buck", "cupids")))) {
			t.Fatalf("results = %v, want The Godfather, then the titles starting with it, then the titles containing it", got)
		}
		if slices.Index(got, id("godfather-buck")) > slices.Index(got, id("cupids")) {
			t.Fatalf("results = %v, want the three-word title before the four-word alias", got)
		}
	})
	t.Run("pages match one page", func(t *testing.T) {
		// "The Godfather Part II" and the four-word alias tie at rank 1/5,
		// which a real rank could not carry across a page boundary.
		for _, query := range []string{"the godfather", "star wars", "the god place"} {
			if got, want := searchPages(t, repo, query, filter), search(t, query); !slices.Equal(got, want) {
				t.Fatalf("%q one row per page = %v, want %v", query, got, want)
			}
		}
	})
	t.Run("repeated words do not outrank shorter titles", func(t *testing.T) {
		got := search(t, "star wars")
		if !slices.Equal(got, ids("star-wars", "star-wars-rebels", "clone-wars", "star-wars-twice")) {
			t.Fatalf("results = %v, want exact, then the shorter leading title, then the rest", got)
		}
	})
}

// searchPages collects a search one result per cursor page.
func searchPages(t *testing.T, repo *ItemRepository, query string, filter AccessFilter) []string {
	t.Helper()
	var ids []string
	var after *SearchCursor
	for range 50 {
		page, err := repo.SearchCursorPage(t.Context(), query, nil, 1, after, filter, false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, contentIDsFromMediaItems(page.Items)...)
		if page.Next == nil {
			return ids
		}
		after = page.Next
	}
	t.Fatalf("%q did not finish paging", query)
	return nil
}

// seedSearchRelevanceFixture adds the titles TestSearchRelevancePostgres
// searches to a new library and returns it with the fixture's content ID for a
// name.
func seedSearchRelevanceFixture(t *testing.T, pool *pgxpool.Pool) (int, func(string) string) {
	t.Helper()
	ctx := t.Context()
	prefix := fmt.Sprintf("search-relevance-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movie',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library)
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	id := func(name string) string { return prefix + "-" + name }
	addAlias := func(name, title string) {
		t.Helper()
		exec(`INSERT INTO media_item_aliases(content_id,title,kind,provider) VALUES($1,$2,'alternate','fixture')`, id(name), title)
	}
	for _, item := range []struct{ name, kind, title, alias string }{
		{"good-place", "series", "The Good Place", ""},
		{"hiding-place", "movie", "The Hiding Place", ""},
		{"dark-place", "movie", "The Dark Place", ""},
		{"secret-place", "movie", "The Secret Place", ""},
		{"palace", "movie", "The Palace", ""},
		{"place-in-the-sun", "movie", "A Place in the Sun", ""},
		{"god-plant", "movie", "The God Plant", ""},
		// Every word of "the god place" appears in this alias, so the item is
		// an exact FTS hit for the misspelled query.
		{"mystic", "movie", "Mystic Land", "Kunlun Kingdom: The God's Secret Place"},
		{"godfather", "movie", "The Godfather", ""},
		{"godfather-2", "movie", "The Godfather Part II", ""},
		{"godfather-buck", "movie", "The Godfather Buck", ""},
		// The repeated "the" gives this alias two covers of "the godfather".
		{"cupids", "movie", "Cauliflower Cupids", "The Godfather & the Lady"},
		{"godfather-comedy", "movie", "Paul Mooney: The Godfather of Comedy", ""},
		{"godfather-coda", "movie", "Mario Puzo's The Godfather, Coda: The Death of Michael Corleone", ""},
		{"star-wars", "movie", "Star Wars", ""},
		{"star-wars-rebels", "series", "Star Wars Rebels", ""},
		{"clone-wars", "movie", "Star Wars: The Clone Wars", ""},
		{"star-wars-twice", "movie", "From Star Wars to Star Wars", ""},
		{"lantern-harbor", "movie", "Lantern Harbor", ""},
		{"harbor-tales", "movie", "Northern Lights", "Tales from the Lantern Keeper's Distant Northern Harbor"},
	} {
		exec(`INSERT INTO media_items(content_id,type,title,year) VALUES($1,$2,$3,2000)`, id(item.name), item.kind, item.title)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id(item.name), library)
		if item.alias != "" {
			addAlias(item.name, item.alias)
		}
	}
	// By whole-query similarity to "lantrn harbor", these short aliases outrank
	// the item's long alias, the only one with both words.
	for _, alias := range []string{"Harbor", "The Harbor", "Old Harbor", "Harbor Days"} {
		addAlias("harbor-tales", alias)
	}
	return library, id
}
