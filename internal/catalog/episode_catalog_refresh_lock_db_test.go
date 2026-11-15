package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// episodeCatalogRaceFixture is one episode titled "Zulu" in a Series library,
// with two 720p files and a catalog entry.
type episodeCatalogRaceFixture struct {
	pool    *pgxpool.Pool
	episode string
	files   [2]int64
}

func newEpisodeCatalogRaceFixture(t *testing.T) episodeCatalogRaceFixture {
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
	f := episodeCatalogRaceFixture{pool: pool, episode: episode}
	for i := range f.files {
		if err := pool.QueryRow(ctx, "INSERT INTO media_files(content_id,episode_id,media_folder_id,file_path,resolution) VALUES($1,$2,$3,$4,'720p') RETURNING id",
			series, episode, folder, fmt.Sprintf("%s-%d.mkv", prefix, i)).Scan(&f.files[i]); err != nil {
			t.Fatal(err)
		}
	}
	if key, codes := f.entry(t); key != "zulu" || fmt.Sprint(codes) != "[720p]" {
		t.Fatalf("seeded entry = %q %v, want zulu [720p]", key, codes)
	}
	return f
}

func (f episodeCatalogRaceFixture) entry(t *testing.T) (sortKey string, resolutionCodes []string) {
	t.Helper()
	if err := f.pool.QueryRow(t.Context(), "SELECT sort_key, resolution_codes FROM episode_catalog_entries WHERE episode_id = $1", f.episode).
		Scan(&sortKey, &resolutionCodes); err != nil {
		t.Fatal(err)
	}
	return sortKey, resolutionCodes
}

// begin opens a transaction and returns it with its backend pid.
func (f episodeCatalogRaceFixture) begin(t *testing.T) (pgx.Tx, int) {
	t.Helper()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int
	if err := tx.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// updateFileBehind runs a file update that must block behind the open
// transaction holder, and returns once it is waiting on holder's locks.
func (f episodeCatalogRaceFixture) updateFileBehind(t *testing.T, holder int, file int64, resolution string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := f.pool.Exec(t.Context(), "UPDATE media_files SET resolution = $1 WHERE id = $2", resolution, file)
		done <- err
	}()
	for tick := time.Tick(10 * time.Millisecond); ; {
		var blocked bool
		if err := f.pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", holder).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return done
		}
		select {
		case err := <-done:
			t.Fatalf("file update finished while the other transaction was open (err = %v)", err)
		case <-tick:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
}

// A file change that refreshes an episode's catalog entry while an edit of
// that episode is still uncommitted waits for the edit and then builds the
// entry from the committed row, so the episode doesn't stay filed under its
// old title (#4239).
func TestEpisodeCatalogRefreshWaitsForAnEpisodeEditPostgres(t *testing.T) {
	f := newEpisodeCatalogRaceFixture(t)
	ctx := t.Context()
	edit, pid := f.begin(t)
	if _, err := edit.Exec(ctx, "UPDATE episodes SET title = 'Bravo' WHERE content_id = $1", f.episode); err != nil {
		t.Fatal(err)
	}
	scanned := f.updateFileBehind(t, pid, f.files[0], "1080p")
	if err := edit.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-scanned; err != nil {
		t.Fatalf("file update: %v", err)
	}
	if key, codes := f.entry(t); key != "bravo" || fmt.Sprint(codes) != "[1080p 720p]" {
		t.Fatalf("entry after the edit and the file refresh = %q %v, want bravo [1080p 720p]", key, codes)
	}
}

// Two transactions that change different files of one episode, one of which
// then edits the episode, both commit, and the entry reflects both files and
// the edit. Refreshes that only share-locked the episode deadlocked here, and
// refreshes that locked nothing kept the first file's old resolution.
func TestEpisodeCatalogRefreshSerializesFileChangesPostgres(t *testing.T) {
	f := newEpisodeCatalogRaceFixture(t)
	ctx := t.Context()
	scan, pid := f.begin(t)
	if _, err := scan.Exec(ctx, "UPDATE media_files SET resolution = '1080p' WHERE id = $1", f.files[0]); err != nil {
		t.Fatal(err)
	}
	other := f.updateFileBehind(t, pid, f.files[1], "2160p")
	if _, err := scan.Exec(ctx, "UPDATE episodes SET title = 'Bravo' WHERE content_id = $1", f.episode); err != nil {
		t.Fatalf("episode edit after the file update: %v", err)
	}
	if err := scan.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Fatalf("other file update: %v", err)
	}
	if key, codes := f.entry(t); key != "bravo" || fmt.Sprint(codes) != "[1080p 2160p]" {
		t.Fatalf("entry after both file changes and the edit = %q %v, want bravo [1080p 2160p]", key, codes)
	}
}
