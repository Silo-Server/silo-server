package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// letterOf mirrors letterBucketSQL for a title key, to check the database
// against the order the cursor pager returns.
func letterOf(key string) string {
	first, _ := utf8.DecodeRuneInString(strings.ToLower(key))
	if i := strings.IndexRune(letterFoldFrom, first); i >= 0 {
		first = []rune(letterFoldTo)[utf8.RuneCountInString(letterFoldFrom[:i])]
	}
	if first >= 'a' && first <= 'z' {
		return strings.ToUpper(string(first))
	}
	return LetterOther
}

// lettersOfList indexes an ordered list of title keys the way LetterIndex
// reports it: non-empty letters in list order with their first position, and
// the runs of consecutive keys under one letter.
func lettersOfList(keys []string) LetterIndex {
	index := LetterIndex{Letters: []LetterBucket{}, Runs: []LetterRun{}}
	at := map[string]int{}
	for position, key := range keys {
		letter := letterOf(key)
		if len(index.Runs) == 0 || index.Runs[len(index.Runs)-1].Letter != letter {
			index.Runs = append(index.Runs, LetterRun{Letter: letter, Position: position})
		}
		if i, ok := at[letter]; ok {
			index.Letters[i].Count++
			continue
		}
		at[letter] = len(index.Letters)
		index.Letters = append(index.Letters, LetterBucket{Letter: letter, Count: 1, Position: position})
	}
	return index
}

// orderedTitleKeys pages the whole query through the cursor pager and returns
// each item's title sort key in list order.
func orderedTitleKeys(t *testing.T, resolver *CatalogResolver, req CatalogRequest, access AccessFilter) []string {
	t.Helper()
	req.CursorPaging = true
	req.Limit = 200
	result, err := resolver.Resolve(t.Context(), req, access)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.HasMore {
		t.Fatal("seeded list should fit one page")
	}
	keys := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		key := strings.Trim(item.SortTitle, " ")
		if key == "" {
			key = item.Title
		}
		keys = append(keys, key)
	}
	return keys
}

// TestLetterIndexMatchesCursorPagesPostgres checks the letter index against
// real rows: titles file under their sort title's initial (articles moved
// away, accents folded, anything else under #), every letter's position is
// where the cursor pager lists its first title, in both directions and when a
// seek jumps there, and the runs match the pager's order whatever the
// database collation does with accented and non-Latin initials. Set SILO_TEST_DATABASE_URL to a migrated
// disposable database; every inserted row is removed on completion.
func TestLetterIndexMatchesCursorPagesPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("letter-index-%d", time.Now().UnixNano())

	var libraryID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`,
		prefix,
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=$1`, libraryID)
	})

	for i, row := range []struct{ title, sortTitle string }{
		{"The Hobbit", "Hobbit, The"},
		{"Hobgoblins", ""},
		{"Titanic", ""},
		{"Élite", ""},
		{"Eagle Eye", ""},
		{"2001: A Space Odyssey", ""},
		{"(500) Days of Summer", ""},
		{"alien", ""},
		{"Avatar", ""},
		{"Zodiac", "  "},
		{"Ōkami", ""},
		{"Șarada", ""},
		{"Амели", ""},
	} {
		id := fmt.Sprintf("%s-%02d", prefix, i)
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, sort_title, year, genres)
			VALUES ($1, 'movie', $2, $3, 2014, '{}'::text[])
		`, id, row.title, row.sortTitle); err != nil {
			t.Fatalf("seed media item %s: %v", row.title, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_item_libraries(content_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`,
			id, libraryID,
		); err != nil {
			t.Fatalf("link media item %s: %v", row.title, err)
		}
	}

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	access := AccessFilter{AllowedLibraryIDs: []int{libraryID}}
	wantCounts := map[string]int{"#": 3, "A": 2, "E": 2, "H": 2, "O": 1, "S": 1, "T": 1, "Z": 1}

	for _, order := range []string{"asc", "desc"} {
		t.Run(order, func(t *testing.T) {
			req := CatalogRequest{
				Source: CatalogSourceQuery,
				Query: QueryDefinition{
					MediaScope: "movie",
					LibraryIDs: []int{libraryID},
					Sort:       QuerySort{Field: querySortTitle, Order: order},
				},
			}
			got, err := resolver.LetterIndex(ctx, req, access)
			if err != nil {
				t.Fatalf("letter index: %v", err)
			}
			want := lettersOfList(orderedTitleKeys(t, resolver, req, access))
			if !slices.Equal(got.Letters, want.Letters) || !slices.Equal(got.Runs, want.Runs) {
				t.Fatalf("letter index = %+v, want the pager's %+v", got, want)
			}
			counts := map[string]int{}
			for _, b := range got.Letters {
				counts[b.Letter] = b.Count
			}
			if fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
				t.Fatalf("letter counts = %v, want %v", counts, wantCounts)
			}
			// Every collation sorts Cyrillic apart from the digits and
			// symbols, so # comes in at least two runs.
			hashRuns := 0
			for _, run := range got.Runs {
				if run.Letter == LetterOther {
					hashRuns++
				}
			}
			if hashRuns < 2 {
				t.Fatalf("runs = %+v, want # split around the Latin letters", got.Runs)
			}

			// The same query is answered from the cache.
			again, err := resolver.LetterIndex(ctx, req, access)
			if err != nil || !slices.Equal(again.Runs, got.Runs) {
				t.Fatalf("second letter index = %+v, %v", again, err)
			}

			// A seek to a letter's position lands on that letter's first
			// title, whether it numbers rows (no snapshot) or walks from a
			// seek anchor (a snapshot the client pins).
			for _, snapshot := range []*time.Time{nil, new(time.Now())} {
				for _, b := range got.Letters {
					seekReq := req
					seekReq.CursorPaging = true
					seekReq.Limit = 1
					seekReq.Seek = new(b.Position)
					seekReq.SnapshotAt = snapshot
					page, err := resolver.Resolve(ctx, seekReq, access)
					if err != nil {
						t.Fatalf("seek to %s: %v", b.Letter, err)
					}
					if len(page.Items) != 1 {
						t.Fatalf("seek to %s at %d returned %d items", b.Letter, b.Position, len(page.Items))
					}
					item := page.Items[0]
					key := strings.Trim(item.SortTitle, " ")
					if key == "" {
						key = item.Title
					}
					if letterOf(key) != b.Letter {
						t.Fatalf("seek to %s at %d landed on %q", b.Letter, b.Position, item.Title)
					}
				}
			}
		})
	}

	t.Run("filters narrow the index", func(t *testing.T) {
		req := CatalogRequest{
			Source: CatalogSourceQuery,
			Query: QueryDefinition{
				MediaScope: "movie",
				LibraryIDs: []int{libraryID},
				Sort:       QuerySort{Field: querySortTitle, Order: "asc"},
				Match:      "all",
				Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{
					{Field: "title", Op: "contains", Value: "a"},
				}}},
			},
		}
		got, err := resolver.LetterIndex(ctx, req, access)
		if err != nil {
			t.Fatalf("letter index: %v", err)
		}
		want := lettersOfList(orderedTitleKeys(t, resolver, req, access))
		if len(want.Letters) == 0 || !slices.Equal(got.Letters, want.Letters) || !slices.Equal(got.Runs, want.Runs) {
			t.Fatalf("filtered letter index = %+v, want the pager's %+v", got, want)
		}
	})

	t.Run("refuses other orders and sources", func(t *testing.T) {
		for name, req := range map[string]CatalogRequest{
			"year order": {Source: CatalogSourceQuery, Query: QueryDefinition{LibraryIDs: []int{libraryID}, Sort: QuerySort{Field: "year"}}},
			"search":     {Source: CatalogSourceQuery, SearchQuery: "hob", Query: QueryDefinition{Sort: QuerySort{Field: querySortTitle}}},
			"favorites":  {Source: CatalogSourceFavorites, Query: QueryDefinition{Sort: QuerySort{Field: querySortTitle}}},
		} {
			if _, err := resolver.LetterIndex(ctx, req, access); err == nil {
				t.Errorf("%s: want an invalid request error", name)
			}
		}
	})
}

// TestLetterIndexEpisodesMatchCursorPagesPostgres checks the episode scope,
// whose title key is the episode relation's own sort key.
func TestLetterIndexEpisodesMatchCursorPagesPostgres(t *testing.T) {
	episodeRepo, seriesID, _ := seedCompletionEpisodes(t, 4)
	pool := episodeRepo.pool
	ctx := t.Context()
	var library int
	if err := pool.QueryRow(ctx, `SELECT el.media_folder_id FROM episode_libraries el JOIN episodes e ON e.content_id = el.episode_id
		WHERE e.series_id = $1 LIMIT 1`, seriesID).Scan(&library); err != nil {
		t.Fatal(err)
	}
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, seriesID, library)
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	access := AccessFilter{AllowedLibraryIDs: []int{library}}
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			MediaScope: "episode",
			LibraryIDs: []int{library},
			Sort:       QuerySort{Field: querySortTitle, Order: "asc"},
		},
	}
	got, err := resolver.LetterIndex(ctx, req, access)
	if err != nil {
		t.Fatalf("letter index: %v", err)
	}
	total := 0
	for _, b := range got.Letters {
		total += b.Count
	}
	req.CursorPaging = true
	req.Limit = 200
	result, err := resolver.Resolve(ctx, req, access)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if total != len(result.Items) || total == 0 {
		t.Fatalf("letter index counts %d episodes, pager lists %d", total, len(result.Items))
	}
	if got.Letters[0].Position != 0 || got.Runs[0].Position != 0 {
		t.Fatalf("first letter starts at %d and first run at %d, want 0", got.Letters[0].Position, got.Runs[0].Position)
	}
}
