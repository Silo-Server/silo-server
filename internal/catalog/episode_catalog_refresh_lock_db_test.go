package catalog

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A file change that refreshes an episode's catalog entry while an edit of
// that episode is still uncommitted waits for the edit and then builds the
// entry from the committed row, so the episode doesn't stay filed under its
// old title (#4239).
func TestEpisodeCatalogRefreshWaitsForAnEpisodeEditPostgres(t *testing.T) {
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
	prefix := fmt.Sprintf("catalog-refresh-lock-%d", time.Now().UnixNano())
	series, episode := prefix+"-series", prefix+"-episode"
	var folder int
	if err := pool.QueryRow(ctx, "INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id", prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, "DELETE FROM media_files WHERE episode_id = $1", episode)
		_, _ = pool.Exec(cleanup, "DELETE FROM episodes WHERE content_id = $1", episode)
		_, _ = pool.Exec(cleanup, "DELETE FROM media_items WHERE content_id = $1", series)
		_, _ = pool.Exec(cleanup, "DELETE FROM media_folders WHERE id = $1", folder)
	})
	exec("INSERT INTO media_items(content_id,type,title,year) VALUES($1,'series','Lock Series',2020)", series)
	exec("INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,'Zulu')", episode, series)
	exec("INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)", episode, folder)
	var fileID int64
	if err := pool.QueryRow(ctx, "INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,resolution) VALUES($1,$2,$3,$4,'720p') RETURNING id",
		series, episode, folder, prefix+".mkv").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	sortKey := func() string {
		t.Helper()
		var key string
		if err := pool.QueryRow(ctx, "SELECT sort_key FROM episode_catalog_entries WHERE episode_id = $1", episode).Scan(&key); err != nil {
			t.Fatal(err)
		}
		return key
	}
	if got := sortKey(); got != "zulu" {
		t.Fatalf("seeded sort_key = %q, want zulu", got)
	}

	edit, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = edit.Rollback(ctx) }()
	if _, err := edit.Exec(ctx, "UPDATE episodes SET title = 'Bravo' WHERE content_id = $1", episode); err != nil {
		t.Fatal(err)
	}
	scanned := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, "UPDATE media_files SET resolution = '1080p' WHERE id = $1", fileID)
		scanned <- err
	}()
	// The file update blocks behind the open edit; commit only once it is
	// waiting, so the refresh it fires overlaps the edit.
	for blocked := false; !blocked; {
		select {
		case err := <-scanned:
			t.Fatalf("file update finished while the episode edit was open (err = %v)", err)
		default:
		}
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted)").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if err := edit.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-scanned; err != nil {
		t.Fatalf("file update: %v", err)
	}
	if got := sortKey(); got != "bravo" {
		t.Fatalf("sort_key after the edit and the file refresh = %q, want bravo", got)
	}
}
