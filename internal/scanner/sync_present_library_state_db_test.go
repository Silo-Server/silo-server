package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSyncPresentLibraryStateScoped covers the scan-scope narrowing of
// syncPresentLibraryState: a scoped run must repair dangling links and restore
// memberships only for files under the scope paths, an empty scope must keep
// the folder-wide behavior, and LIKE wildcard characters in a scope path must
// not leak into sibling directories.
func TestSyncPresentLibraryStateScoped(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	seriesA := fmt.Sprintf("sync-series-a-%d", suffix)
	seriesB := fmt.Sprintf("sync-series-b-%d", suffix)
	epA := fmt.Sprintf("sync-ep-a-%d", suffix)
	epB := fmt.Sprintf("sync-ep-b-%d", suffix)
	// dirA contains LIKE wildcards; dirTrap is the literal-collision sibling
	// that an unescaped '%'/'_' would wrongly match.
	root := fmt.Sprintf("/sync-test-%d", suffix)
	dirA := root + "/Show_100%/Season 01"
	dirTrap := root + "/ShowX100Y/Season 01"
	dirB := root + "/Other Show/Season 01"

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'Sync Scope Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{seriesA, seriesB})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, s := range []struct{ id, title string }{{seriesA, "Show A"}, {seriesB, "Show B"}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres)
			VALUES ($1, 'series', $2, 'matched', '{}'::text[])
		`, s.id, s.title); err != nil {
			t.Fatalf("seed series %s: %v", s.id, err)
		}
	}
	for _, e := range []struct {
		id, series string
	}{{epA, seriesA}, {epB, seriesB}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, 1, 'Ep')
		`, e.id, e.series); err != nil {
			t.Fatalf("seed episode %s: %v", e.id, err)
		}
	}

	seedFile := func(dir, episodeID, danglingContentID string) {
		t.Helper()
		var contentID *string
		if danglingContentID != "" {
			contentID = &danglingContentID
		}
		var epID *string
		if episodeID != "" {
			epID = &episodeID
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, content_id, episode_id)
			VALUES ($1, $2, 1024, $3, $4)
		`, folderID, dir+"/ep.mkv", contentID, epID); err != nil {
			t.Fatalf("seed file in %s: %v", dir, err)
		}
	}
	danglingRef := func(dir string) string {
		var v *string
		if err := pool.QueryRow(ctx, `
			SELECT content_id FROM media_files WHERE media_folder_id = $1 AND file_path = $2
		`, folderID, dir+"/ep.mkv").Scan(&v); err != nil {
			t.Fatalf("read content_id for %s: %v", dir, err)
		}
		if v == nil {
			return ""
		}
		return *v
	}
	membershipCount := func(episodeID string) int {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM episode_libraries WHERE media_folder_id = $1 AND episode_id = $2
		`, folderID, episodeID).Scan(&n); err != nil {
			t.Fatalf("count membership for %s: %v", episodeID, err)
		}
		return n
	}

	// FKs on media_files.content_id? There is none (content_id is a loose
	// pointer), so a nonexistent id models the dangling state directly.
	danglingA := fmt.Sprintf("gone-a-%d", suffix)
	danglingB := fmt.Sprintf("gone-b-%d", suffix)
	seedFile(dirA, epA, danglingA)     // in scope: dangling pointer + membership to restore
	seedFile(dirTrap, "", danglingB)   // wildcard trap: must stay untouched by scoped run
	seedFile(dirB, epB, "")            // out of scope: membership must not be restored by scoped run

	scanner := &Scanner{fileRepo: NewFileRepository(pool)}

	// Scoped run over dirA only.
	if err := scanner.syncPresentLibraryState(ctx, folderID, []string{dirA}); err != nil {
		t.Fatalf("scoped sync: %v", err)
	}
	if got := danglingRef(dirA); got != "" {
		t.Errorf("in-scope dangling content_id = %q, want cleared", got)
	}
	if got := danglingRef(dirTrap); got != danglingB {
		t.Errorf("wildcard-sibling content_id = %q, want untouched %q", got, danglingB)
	}
	if got := membershipCount(epA); got != 1 {
		t.Errorf("in-scope episode membership rows = %d, want 1", got)
	}
	if got := membershipCount(epB); got != 0 {
		t.Errorf("out-of-scope episode membership rows = %d, want 0 after scoped sync", got)
	}
	var latestA *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT latest_episode_added_at FROM media_items WHERE content_id = $1
	`, seriesA).Scan(&latestA); err != nil || latestA == nil {
		t.Errorf("latest_episode_added_at for in-scope series = %v (err %v), want set", latestA, err)
	}

	// Folder-wide run (nil scope) repairs the rest.
	if err := scanner.syncPresentLibraryState(ctx, folderID, nil); err != nil {
		t.Fatalf("folder-wide sync: %v", err)
	}
	if got := danglingRef(dirTrap); got != "" {
		t.Errorf("folder-wide sync left dangling content_id = %q, want cleared", got)
	}
	if got := membershipCount(epB); got != 1 {
		t.Errorf("out-of-scope episode membership rows after folder-wide sync = %d, want 1", got)
	}
}
