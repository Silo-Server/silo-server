package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSearchCursorPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := NewItemRepository(pool)
	prefix := fmt.Sprintf("search-cursor-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, "INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id", prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM media_items WHERE content_id LIKE $1", prefix+"%")
		_, _ = pool.Exec(ctx, "DELETE FROM media_folders WHERE id=$1", library)
	}()
	add := func(i int, title string, year int) string {
		id := fmt.Sprintf("%s-%03d", prefix, i)
		if _, err := pool.Exec(ctx, "INSERT INTO media_items(content_id,type,title,year) VALUES($1,'movie',$2,$3)", id, title, year); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)", id, library); err != nil {
			t.Fatal(err)
		}
		return id
	}
	filter := AccessFilter{AllowedLibraryIDs: []int{library}}
	for i := range 8 {
		add(i, "Quasar Adventure", 2000+i)
	}
	traverse := func(query string, total bool, options ...SearchCursorOptions) []string {
		var after *SearchCursor
		got := []string{}
		for range 40 {
			page, err := repo.SearchCursorPage(ctx, query, []string{"movie"}, 2, after, filter, total, options...)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				got = append(got, item.ContentID)
			}
			if !page.HasMore {
				return got
			}
			if page.Next == nil {
				t.Fatal("missing next")
			}
			after = page.Next
		}
		t.Fatal("cursor did not terminate")
		return nil
	}
	want := traverse("Quasar", true)
	if len(want) != 8 {
		t.Fatalf("fts count=%d", len(want))
	}
	if got := traverse("Quasar", false); !reflect.DeepEqual(got, want) {
		t.Fatalf("skip_total changed traversal: %v vs %v", got, want)
	}
	seedPage, err := repo.SearchCursorPage(ctx, "Quasar", []string{"movie"}, 2, &SearchCursor{Mode: "fts"}, filter, false)
	if err != nil || seedPage.Items[0].ContentID != want[0] {
		t.Fatalf("mode-only seed failed: %v", err)
	}
	limited := traverse("Quasar", true, SearchCursorOptions{Definition: QueryDefinition{Limit: new(3)}})
	if len(limited) != 3 {
		t.Fatalf("cap returned %d", len(limited))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.SearchCursorPage(canceled, "Quasar", nil, 2, nil, filter, false); err == nil {
		t.Fatal("canceled search succeeded")
	}
	boundary, err := repo.SeekSearchCursor(ctx, "Quasar", []string{"movie"}, 3, &SearchCursor{Mode: "fts"}, filter)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.SearchCursorPage(ctx, "Quasar", []string{"movie"}, 2, boundary, filter, true)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 8 || page.Items[0].ContentID != want[3] {
		t.Fatalf("jump wrong: %+v", page)
	}
	// Deleting a boundary cannot shift the next tuple.
	if _, err := pool.Exec(ctx, "DELETE FROM media_items WHERE content_id=$1", want[2]); err != nil {
		t.Fatal(err)
	}
	page, err = repo.SearchCursorPage(ctx, "Quasar", []string{"movie"}, 2, boundary, filter, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].ContentID != want[3] {
		t.Fatal("deleted boundary shifted continuation")
	}
	def := QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "year", Op: "gte", Value: 2005}}}}}
	if got := traverse("Quasar", true, SearchCursorOptions{Definition: def}); len(got) != 3 {
		t.Fatalf("filtered search got %d", len(got))
	}
	ordered := SearchCursorOptions{Definition: QueryDefinition{Sort: QuerySort{Field: "year", Order: "desc"}}}
	if got := traverse("Quasar", true, ordered); len(got) != 7 || got[0] != want[7] {
		t.Fatalf("custom sort wrong %v", got)
	}
	sortedBoundary, err := repo.SeekSearchCursor(ctx, "Quasar", []string{"movie"}, 2, nil, filter, ordered)
	if err != nil {
		t.Fatal(err)
	}
	sortedPage, err := repo.SearchCursorPage(ctx, "Quasar", []string{"movie"}, 2, sortedBoundary, filter, true, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if sortedPage.Items[0].ContentID != want[5] {
		t.Fatalf("sorted jump wrong %s", sortedPage.Items[0].ContentID)
	}
	for i := range 60 {
		add(100+i, "Game of Thrones", 2000)
	}
	fuzzy := traverse("Gane of Throns", true)
	baseline, _, _, _, err := repo.SearchPage(ctx, "Gane of Throns", []string{"movie"}, 200, 0, filter, true)
	if err != nil {
		t.Fatal(err)
	}
	baselineIDs := make([]string, len(baseline))
	for i, item := range baseline {
		baselineIDs[i] = item.ContentID
	}
	if !reflect.DeepEqual(fuzzy, baselineIDs) {
		t.Fatal("fuzzy cursor differs from existing bounded rerank")
	}
	if len(fuzzy) != 50 {
		t.Fatalf("fuzzy cap=%d", len(fuzzy))
	}
	if got := traverse("Gane of Throns", false); !reflect.DeepEqual(got, fuzzy) {
		t.Fatal("fuzzy skip_total changed ordering")
	}
	seed, err := repo.SearchCursorPage(ctx, "Gane of Throns", []string{"movie"}, 2, nil, filter, false)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Scope.Mode != "combined" {
		t.Fatalf("wrong fuzzy scope %v", seed.Scope)
	}
	for i := range 5 {
		add(200+i, "Gane of Throns", 2000)
	}
	_, err = repo.SearchCursorPage(ctx, "Gane of Throns", []string{"movie"}, 2, seed.Next, filter, false)
	if !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("family change should reject, got %v", err)
	}
}

func TestSearchCursorMixedAndGroupsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	prefix := fmt.Sprintf("search-mixed-%d", time.Now().UnixNano())
	var lib int
	if err := pool.QueryRow(ctx, "INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id", prefix).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM media_items WHERE content_id LIKE $1", prefix+"%")
		_, _ = pool.Exec(ctx, "DELETE FROM literary_works WHERE work_id LIKE $1", prefix+"%")
		_, _ = pool.Exec(ctx, "DELETE FROM media_folders WHERE id=$1", lib)
	}()
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	series := prefix + "series"
	exec("INSERT INTO media_items(content_id,type,title) VALUES($1,'series','Mixed Fixture')", series)
	exec("INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)", series, lib)
	for i := range 6 {
		id := fmt.Sprintf("%s-episode-%d", prefix, i)
		exec("INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,air_date) VALUES($1,$2,1,$3,'Quasar Adventure','2025-01-01')", id, series, i+1)
		exec("INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)", id, lib)
	}
	for i := range 6 {
		work := fmt.Sprintf("%s-work-%d", prefix, i/2)
		if i%2 == 0 {
			exec("INSERT INTO literary_works(work_id,canonical_title,normalized_title) VALUES($1,'Quasar Adventure','quasar adventure')", work)
		}
		id := fmt.Sprintf("%s-book-%d", prefix, i)
		exec("INSERT INTO media_items(content_id,type,title,year) VALUES($1,'ebook','Quasar Adventure',$2)", id, 2000+i)
		exec("INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)", id, lib)
		exec("INSERT INTO literary_work_items(work_id,content_id,format_type,link_source) VALUES($1,$2,'ebook','manual')", work, id)
	}
	repo := NewItemRepository(pool)
	filter := AccessFilter{AllowedLibraryIDs: []int{lib}}
	for _, field := range []string{"", "year"} {
		options := SearchCursorOptions{GroupByWork: true, Definition: QueryDefinition{Limit: new(3), Sort: QuerySort{Field: field, Order: "desc"}}}
		page, err := repo.SearchCursorPage(ctx, "Quasar", []string{"ebook"}, 1, nil, filter, true, options)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 2 || !page.HasMore {
			t.Fatalf("group raw cap field=%s total=%d hasMore=%v", field, page.Total, page.HasMore)
		}
		next, err := repo.SearchCursorPage(ctx, "Quasar", []string{"ebook"}, 1, page.Next, filter, true, options)
		if err != nil {
			t.Fatal(err)
		}
		if next.HasMore || len(next.Items) != 1 || next.Items[0].ContentID == page.Items[0].ContentID {
			t.Fatalf("group cap boundary field=%s: %+v", field, next)
		}
	}

	for _, sort := range []string{"", "year", "added_at"} {
		options := SearchCursorOptions{GroupByWork: true, Definition: QueryDefinition{Sort: QuerySort{Field: sort, Order: "desc"}}}
		seen := map[string]bool{}
		var cursor *SearchCursor
		for range 10 {
			page, err := repo.SearchCursorPage(ctx, "Quasar", nil, 2, cursor, filter, true, options)
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 9 {
				t.Fatalf("sort %s total=%d expected 6 episodes+3 works", sort, page.Total)
			}
			for _, item := range page.Items {
				if seen[item.ContentID] {
					t.Fatal("duplicate representative")
				}
				seen[item.ContentID] = true
			}
			if !page.HasMore {
				break
			}
			cursor = page.Next
		}
		if len(seen) != 9 {
			t.Fatalf("sort %s traversed %d", sort, len(seen))
		}
	}
}
