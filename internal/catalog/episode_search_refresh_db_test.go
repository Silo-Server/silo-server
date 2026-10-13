package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Function-call budgets observe work inside triggers, which pgx statement
// counts cannot see. Transaction-local counters isolate concurrent test runs.
func TestEpisodeSearchRefreshWorkPostgres(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("SET LOCAL track_functions = 'all'")
	prefix := fmt.Sprintf("search-refresh-%d", time.Now().UnixNano())
	series, episode := prefix+"-series", prefix+"-episode"
	var folder, otherFolder int
	if err := tx.QueryRow(ctx, "INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id", prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id", prefix+"-other").Scan(&otherFolder); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO media_items(content_id,type,title,year) VALUES($1,'series','Refresh Series',2020)", series)
	exec("INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview) VALUES($1,$2,1,1,'Pilot','A buried signal returns.')", episode, series)
	exec("INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)", episode, folder)
	var fileID int64
	if err := tx.QueryRow(ctx, "INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,resolution) VALUES($1,$2,$3,$4,'720p') RETURNING id", series, episode, folder, prefix+".mkv").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	refreshCalls := func() int64 {
		t.Helper()
		var calls int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(calls),0)::bigint FROM pg_stat_xact_user_functions WHERE funcname='refresh_episode_catalog_entry'`).Scan(&calls); err != nil {
			t.Fatal(err)
		}
		return calls
	}
	checkWork := func(name, sql string, expected int64, args ...any) {
		t.Helper()
		before := refreshCalls()
		exec(sql, args...)
		if calls := refreshCalls() - before; calls != expected {
			t.Fatalf("%s refreshed %d entries, want %d", name, calls, expected)
		}
	}
	checkWork("unchanged episode metadata", "UPDATE episodes SET title=title,overview=overview,runtime=runtime WHERE content_id=$1", 0, episode)
	markOldTimestamp := func() {
		t.Helper()
		exec("UPDATE episode_catalog_entries SET updated_at='2000-01-01Z'::timestamptz WHERE episode_id=$1", episode)
	}
	checkTimestamp := func() {
		t.Helper()
		var current bool
		if err := tx.QueryRow(ctx, "SELECT BOOL_AND(updated_at>'2000-01-01Z'::timestamptz) FROM episode_catalog_entries WHERE episode_id=$1", episode).Scan(&current); err != nil || !current {
			t.Fatalf("metadata edit did not advance catalog timestamps: current=%v err=%v", current, err)
		}
	}
	markOldTimestamp()
	checkWork("title and overview together", "UPDATE episodes SET title='Dune Part Two',overview='An ancient beacon echoes.' WHERE content_id=$1", 1, episode)
	checkTimestamp()
	checkDocument := func(title, overview string) {
		t.Helper()
		var matches bool
		if err := tx.QueryRow(ctx, `SELECT BOOL_AND(search_title_normalized=public.normalize_search_text($2)
			AND search_title_vector=setweight(to_tsvector('simple',public.normalize_search_text($2)),'A')
			AND search_overview_vector=to_tsvector('english',$3))
			FROM episode_catalog_entries WHERE episode_id=$1`, episode, title, overview).Scan(&matches); err != nil {
			t.Fatal(err)
		}
		if !matches {
			t.Fatal("episode search documents differ from current source metadata")
		}
	}
	checkDocument("Dune Part Two", "An ancient beacon echoes.")
	markOldTimestamp()
	checkWork("overview only", "UPDATE episodes SET overview='A hidden beacon awakens.' WHERE content_id=$1", 0, episode)
	checkTimestamp()
	checkDocument("Dune Part Two", "A hidden beacon awakens.")
	checkWork("file attributes", "UPDATE media_files SET resolution='1080p' WHERE id=$1", 1, fileID)
	checkDocument("Dune Part Two", "A hidden beacon awakens.")
	checkWork("unchanged series metadata", "UPDATE media_items SET year=year,genres=genres,status=status WHERE content_id=$1", 0, series)
	checkWork("series facets", "UPDATE media_items SET year=2021,genres=ARRAY['Drama'] WHERE content_id=$1", 1, series)
	checkDocument("Dune Part Two", "A hidden beacon awakens.")
	var facetMatches bool
	if err := tx.QueryRow(ctx, "SELECT year=2021 AND genres=ARRAY['Drama'] FROM episode_catalog_entries WHERE episode_id=$1 AND media_folder_id=$2", episode, folder).Scan(&facetMatches); err != nil || !facetMatches {
		t.Fatalf("series facets did not refresh: match=%v err=%v", facetMatches, err)
	}
	checkWork("file membership move", "UPDATE media_files SET media_folder_id=$2 WHERE id=$1", 2, fileID, otherFolder)
	checkDocument("Dune Part Two", "A hidden beacon awakens.")
	checkWork("overview with null", "UPDATE episodes SET overview=NULL WHERE content_id=$1", 0, episode)
	checkDocument("Dune Part Two", "")
	// Facet/file maintenance must reuse both documents when the title and
	// overview inputs did not change. A transaction-local sentinel reveals
	// recomputation without relying on timings of built-in SQL functions.
	exec("UPDATE episode_catalog_entries SET search_title_vector='sentinel'::tsvector,search_overview_vector='sentinel'::tsvector WHERE episode_id=$1", episode)
	checkWork("file change reuses documents", "UPDATE media_files SET hdr=true WHERE id=$1", 1, fileID)
	var reused bool
	if err := tx.QueryRow(ctx, `SELECT BOOL_AND(search_title_vector='sentinel'::tsvector AND search_overview_vector='sentinel'::tsvector)
		FROM episode_catalog_entries WHERE episode_id=$1`, episode).Scan(&reused); err != nil || !reused {
		t.Fatalf("file refresh rebuilt unchanged search documents: reused=%v err=%v", reused, err)
	}
	exec("UPDATE episode_catalog_entries SET title=title,search_title_vector=NULL,search_overview_vector=NULL WHERE episode_id=$1", episode)
	checkDocument("Dune Part Two", "")
	otherSeries := prefix + "-other-series"
	exec("INSERT INTO media_items(content_id,type,title,year) VALUES($1,'series','Other Series',2025)", otherSeries)
	exec("UPDATE episodes SET series_id=$2 WHERE content_id=$1", episode, otherSeries)
	var parentMatches bool
	if err := tx.QueryRow(ctx, "SELECT BOOL_AND(series_id=$2 AND year=2025) FROM episode_catalog_entries WHERE episode_id=$1", episode, otherSeries).Scan(&parentMatches); err != nil || !parentMatches {
		t.Fatalf("changed episode parent did not refresh: match=%v err=%v", parentMatches, err)
	}
	newEpisode := prefix + "-reanchored"
	exec("UPDATE episodes SET content_id=$2 WHERE content_id=$1", episode, newEpisode)
	episode = newEpisode
	checkDocument("Dune Part Two", "")
	exec("DELETE FROM episodes WHERE content_id=$1", episode)
	var remaining int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM episode_catalog_entries WHERE episode_id=$1", episode).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("deleted episode left catalog entries: count=%d err=%v", remaining, err)
	}
}
