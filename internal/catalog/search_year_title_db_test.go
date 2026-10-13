package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// Providers disambiguate remakes and reboots with the year ("Castle (2009)").
// Searching the bare title must treat that item as an exact title match on the
// cursor and offset paths, including the short whole-title path ("Spy").
func TestSearchYearSuffixedTitleIsExactPostgres(t *testing.T) {
	pool := searchOptimizationPool(t, nil)
	ctx := t.Context()
	prefix := fmt.Sprintf("search-year-title-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
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
	for _, item := range []struct {
		name, kind, title, original string
		year                        int
	}{
		{"castle-2009", "series", "Castle (2009)", "", 2009},
		{"castle-1999", "series", "Castle 1999", "", 2009},
		// Repeated words outscore the series on title prefix rank, which
		// buried it below every partial title before it counted as exact.
		{"castle-castle", "series", "Castle & Castle", "", 2018},
		{"the-castle", "movie", "The Castle", "The Castle", 1997},
		{"castle-freak", "movie", "Castle Freak", "", 1996},
		{"parent", "series", "Fixture Parent", "", 2001},
		{"spy-2015", "movie", "Spy (2015)", "", 2015},
		{"spyder", "movie", "Spyder", "", 2017},
	} {
		exec(`INSERT INTO media_items(content_id,type,title,original_title,year) VALUES($1,$2,$3,$4,$5)`, id(item.name), item.kind, item.title, item.original, item.year)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id(item.name), library)
	}
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,'Castle')`, id("episode"), id("parent"))
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, id("episode"), library)

	repo := NewItemRepository(pool)
	filter := AccessFilter{AllowedLibraryIDs: []int{library}}
	search := func(query string) (cursor, offset []string) {
		t.Helper()
		page, err := repo.SearchCursorPage(ctx, query, nil, 20, nil, filter, false)
		if err != nil {
			t.Fatal(err)
		}
		items, _, err := repo.Search(ctx, query, nil, 20, 0, filter)
		if err != nil {
			t.Fatal(err)
		}
		return contentIDsFromMediaItems(page.Items), contentIDsFromMediaItems(items)
	}

	for _, tc := range []struct {
		query string
		want  func(t *testing.T, ids []string)
	}{
		{query: "Castle", want: func(t *testing.T, ids []string) {
			// The literal "Castle" episode and the year-suffixed series share
			// the exact tier ahead of every partial title.
			if top := ids[:min(2, len(ids))]; !slices.Contains(top, id("castle-2009")) || !slices.Contains(top, id("episode")) {
				t.Fatalf("exact tier = %v, want the series and the episode", top)
			}
			if slices.Index(ids, id("castle-1999")) < 2 {
				t.Fatalf("a year suffix that is not the item's year counted as exact: %v", ids)
			}
		}},
		{query: "Castle 2009", want: func(t *testing.T, ids []string) {
			if len(ids) == 0 || ids[0] != id("castle-2009") {
				t.Fatalf("year hint results = %v, want the 2009 series first", ids)
			}
		}},
		{query: "Spy", want: func(t *testing.T, ids []string) {
			if !slices.Equal(ids, []string{id("spy-2015")}) {
				t.Fatalf("short whole-title results = %v, want only the year-suffixed title", ids)
			}
		}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			cursor, offset := search(tc.query)
			tc.want(t, cursor)
			if !slices.Equal(cursor, offset) {
				t.Fatalf("cursor results %v differ from offset results %v", cursor, offset)
			}
		})
	}
}
