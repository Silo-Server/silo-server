package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecentTVCursorFinalEventTuple(t *testing.T) {
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
	suffix := fmt.Sprintf("recent-cursor-%d", time.Now().UnixNano())
	id := func(s string) string { return suffix + "-" + s }
	base := time.Date(2026, 8, 8, 10, 0, 0, 123456000, time.UTC)
	snapshot := base.Add(time.Second)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, suffix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	series := []string{id("a"), id("b"), id("c"), id("d"), id("missing"), id("denied"), id("late")}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_items WHERE content_id=ANY($1)`, series)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	for _, s := range series {
		exec(`INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'series','Cursor Series','matched','{}')`, s)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id,first_seen_at) VALUES($1,$2,$3)`, s, folder, base)
	}
	// Two grouped runs of the same show have identical time/type/target, so only
	// event_id can distinguish their continuation boundary.
	addEpisode := func(name, seriesName, runName string, number int, at time.Time, missing bool) {
		t.Helper()
		var runID any
		if runName != "" {
			runID = id(runName)
			exec(`INSERT INTO scan_runs(id,media_folder_id,mode,status,requested_at) VALUES($1,$2,'library','completed',$3) ON CONFLICT DO NOTHING`, runID, folder, at)
		}
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,$3,'Cursor Episode')`, id(name), id(seriesName), number)
		exec(`INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at,first_seen_scan_run_id) VALUES($1,$2,$3,$4)`, id(name), folder, at, runID)
		exec(`INSERT INTO media_files(episode_id,media_folder_id,file_path,file_size,missing_since) VALUES($1,$2,$3,100,CASE WHEN $4 THEN now() ELSE NULL END)`, id(name), folder, "/synthetic/"+id(name), missing)
	}
	addEpisode("a1", "a", "run-a", 1, base, false)
	addEpisode("a2", "a", "run-a", 2, base, false)
	addEpisode("a3", "a", "run-b", 3, base, false)
	addEpisode("a4", "a", "run-b", 4, base, false)
	addEpisode("b1", "b", "run-c", 1, base, false)
	addEpisode("c1", "c", "", 1, base, false)
	addEpisode("missing1", "missing", "run-missing", 1, base, true)
	addEpisode("denied1", "denied", "run-denied", 1, base, false)
	exec(`UPDATE media_item_libraries SET first_seen_at=$1 WHERE content_id=$2`, base.Add(time.Hour), id("late"))
	allowed := series[:5]
	repo := NewRecentTVRepository(pool)
	for _, prefix := range []string{"", "Cursor"} {
		for _, unique := range []bool{false, true} {
			for _, skipTotal := range []bool{false, true} {
				t.Run(fmt.Sprintf("prefix=%s/unique=%t/skip=%t", prefix, unique, skipTotal), func(t *testing.T) {
					q := RecentTVQuery{LibraryIDs: []int{folder}, Access: AccessFilter{AllowedContentIDs: allowed}, SnapshotAt: &snapshot, NamePrefix: prefix, UniqueTargets: unique, Limit: 50}
					expected, total, _, err := repo.List(ctx, q)
					if err != nil {
						t.Fatal(err)
					}
					want := 6
					if unique {
						want = 5
					}
					if len(expected) != want || total != want {
						t.Fatalf("baseline events=%d total=%d want=%d", len(expected), total, want)
					}
					if expected[0].ContentID != id("b1") {
						t.Fatalf("episode tie-break first=%+v", expected[0])
					}
					foundNull := false
					for _, target := range expected {
						if target.ContentID == id("c") {
							foundNull = target.EventID == ""
						}
						if target.ContentID == id("denied") || target.ContentID == id("denied1") || target.ContentID == id("late") || target.ContentID == id("missing1") {
							t.Fatalf("out of scope event: %+v", target)
						}
					}
					if !foundNull {
						t.Fatal("NULL scan run not normalized")
					}
					q.CursorPaging = true
					q.Limit = 1
					q.SkipTotal = skipTotal
					var got []RecentTVTarget
					for range len(expected) + 1 {
						page, count, more, err := repo.List(ctx, q)
						if err != nil {
							t.Fatal(err)
						}
						if !skipTotal && count != total {
							t.Fatalf("continuation count=%d want%d", count, total)
						}
						got = append(got, page...)
						if !more {
							break
						}
						if len(page) != 1 {
							t.Fatalf("nonterminal page=%v", page)
						}
						q.After = recentTVCursor(page[0], len(got))
					}
					if !reflect.DeepEqual(got, expected) {
						t.Fatalf("cursor pages=%+v\nwant=%+v", got, expected)
					}
					for _, index := range []int{0, 2, len(expected), len(expected) + 1} {
						q.Seek = new(index)
						q.After = recentTVCursor(expected[0], 1)
						page, _, _, err := repo.List(ctx, q)
						if err != nil {
							t.Fatal(err)
						}
						want := expected[min(index, len(expected)):min(index+1, len(expected))]
						if len(page) != len(want) || (len(page) > 0 && !reflect.DeepEqual(page, want)) {
							t.Fatalf("seek%d page=%+v want=%+v", index, page, want)
						}
					}
				})
			}
		}
	}
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	request := CatalogRequest{CursorPaging: true, Limit: 1, SnapshotAt: &snapshot}
	section := catalogPageSection{Scope: "library", LibraryID: &folder}
	result, handled, err := resolver.resolveRecentTVSectionSource(ctx, request, AccessFilter{AllowedContentIDs: allowed}, section)
	if err != nil || !handled || result == nil || !result.HasMore || result.Next == nil || len(result.Items) != 1 {
		t.Fatalf("resolver first result=%+v handled=%v err=%v", result, handled, err)
	}
	if result.Next.Consumed != 1 || len(result.Next.Keys) != 4 {
		t.Fatalf("resolver tuple=%+v", result.Next)
	}
	request.After = result.Next
	second, _, err := resolver.resolveRecentTVSectionSource(ctx, request, AccessFilter{AllowedContentIDs: allowed}, section)
	if err != nil || len(second.Items) != 1 || second.Items[0].ContentID != id("a") || second.Next == nil || second.Next.Consumed != 2 {
		t.Fatalf("resolver continuation=%+v err=%v", second, err)
	}

	// A consumed boundary remains usable after its episode disappears. A newer
	// event inserted after the first-page fence must not enter the continuation.
	q := RecentTVQuery{LibraryIDs: []int{folder}, Access: AccessFilter{AllowedContentIDs: allowed}, SnapshotAt: &snapshot, CursorPaging: true, Limit: 1}
	first, _, more, err := repo.List(ctx, q)
	if err != nil || !more || len(first) != 1 {
		t.Fatalf("first=%v more=%v err=%v", first, more, err)
	}
	q.After = recentTVCursor(first[0], 1)
	exec(`DELETE FROM media_files WHERE episode_id=$1`, id("b1"))
	exec(`DELETE FROM media_item_libraries WHERE content_id=$1`, id("b"))
	addEpisode("new", "a", "run-new", 5, snapshot.Add(time.Hour), false)
	page, _, _, err := repo.List(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ContentID != id("a") || page[0].EventID != id("run-a") {
		t.Fatalf("continuation after deleted boundary: %+v", page)
	}
	q.After = &QueryCursor{Keys: []QueryCursorValue{{Kind: "text", Value: new("bad")}}}
	if _, _, _, err := repo.List(ctx, q); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}
