package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRootCoverageClauses(t *testing.T) {
	t.Parallel()

	const moviesRoot = "/mnt/movies"
	clauses, args := rootCoverageClauses([]string{moviesRoot, "/mnt/tv_shows"}, 3)
	if len(clauses) != 2 {
		t.Fatalf("clauses len = %d, want 2 (%v)", len(clauses), clauses)
	}
	if clauses[0] != `(file_path = $3 OR file_path LIKE $4 ESCAPE '\')` {
		t.Fatalf("clauses[0] = %q", clauses[0])
	}
	if clauses[1] != `(file_path = $5 OR file_path LIKE $6 ESCAPE '\')` {
		t.Fatalf("clauses[1] = %q", clauses[1])
	}

	want := []any{moviesRoot, moviesRoot + "/%", "/mnt/tv_shows", `/mnt/tv\_shows/%`}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %v, want %v", i, args[i], want[i])
		}
	}

	// The prefix pattern must end with a separator so a sibling root sharing a
	// string prefix (/mnt/movies2) can never match /mnt/movies.
	pattern, ok := args[1].(string)
	if !ok || !strings.HasSuffix(pattern, string(filepath.Separator)+"%") {
		t.Fatalf("prefix pattern %v does not enforce a path separator boundary", args[1])
	}

	if clauses, args := rootCoverageClauses(nil, 1); len(clauses) != 0 || len(args) != 0 {
		t.Fatalf("rootCoverageClauses(nil) = %v, %v; want empty", clauses, args)
	}
}

func newScannerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedDeadRootTestFolder(t *testing.T, pool *pgxpool.Pool, folderType, name string) int {
	t.Helper()
	ctx := context.Background()
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
		folderType, name,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return folderID
}

// TestCollectLogicalFilePathsReportsUnreadableEntries covers Codex review
// finding #3 on PR #472: the video walk swallowed per-entry read failures and
// reported no signal, so a mount dying partway through traversal produced a
// short file list indistinguishable from a large deletion.
func TestCollectLogicalFilePathsReportsUnreadableEntries(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}

	root := t.TempDir()
	readable := filepath.Join(root, "Alpha (2020)")
	if err := os.MkdirAll(readable, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readable, "Alpha (2020).mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A subtree the walk cannot read stands in for the portion of a tree that
	// becomes unreachable when a mount dies mid-traversal.
	blocked := filepath.Join(root, "Beta (2021)")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("mkdir blocked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "Beta (2021).mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocked: %v", err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{root}, "movies", nil)
	if err != nil {
		t.Fatalf("collectLogicalFilePaths: %v", err)
	}
	if len(walkFailures) != 1 {
		t.Fatalf("walkFailures = %v, want exactly the unreadable subtree; a partial listing "+
			"would otherwise be treated as an authoritative inventory", walkFailures)
	}
	// The failure is scoped to the subtree that could not be read, not to the
	// library root — otherwise one permanently broken entry would suppress
	// missing-file reconciliation for the whole root on every future scan.
	if walkFailures[0] != blocked {
		t.Fatalf("walkFailures[0] = %q, want the blocked subtree %q", walkFailures[0], blocked)
	}
	if walkFailures[0] == root {
		t.Fatal("failure was recorded against the library root; protection must be scoped to the subtree")
	}
	// The readable file is still found — one bad subtree must not abort the walk.
	if len(files) != 1 {
		t.Fatalf("files = %v, want just the readable one", files)
	}
}

// A subtree scan scoped to a folder that was deleted must see an empty scope,
// not an unreadable one, as long as its library root is still there: the full
// library scan already reconciles the same deletion. Anything that is not a
// confirmed absence under a reachable root stays a walk failure.
func TestCollectLogicalFilePathsVanishedScopeRoot(t *testing.T) {
	t.Parallel()

	library := t.TempDir()
	if err := os.WriteFile(filepath.Join(library, "Other (2002).mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	dangling := filepath.Join(library, "Linked (2003)")
	if err := os.Symlink(filepath.Join(library, "nowhere"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	goneRoot := filepath.Join(t.TempDir(), "unmounted")

	cases := []struct {
		name         string
		scope        string
		libraryRoot  string
		wantFailures int
	}{
		{"deleted folder under reachable root", filepath.Join(library, "Harry Test (2001)"), library, 0},
		{"deleted folder whose parent is gone too", filepath.Join(library, "Show", "Season 01"), library, 1},
		{"library root itself is gone", filepath.Join(goneRoot, "Harry Test (2001)"), goneRoot, 1},
		{"scope is the library root", goneRoot, goneRoot, 1},
		{"dangling symlink scope", dangling, library, 1},
		{"scope below a dangling symlink", filepath.Join(dangling, "Season 01"), library, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{tc.scope}, "movies", []string{tc.libraryRoot})
			if err != nil {
				t.Fatalf("collectLogicalFilePaths: %v", err)
			}
			if len(files) != 0 {
				t.Fatalf("files = %v, want none", files)
			}
			if len(walkFailures) != tc.wantFailures {
				t.Fatalf("walkFailures = %v, want %d", walkFailures, tc.wantFailures)
			}
		})
	}
}

// Only a confirmed absence makes a vanished scope empty: a scope the scanner
// cannot even stat (permission denied here, EIO or a hung mount in the field)
// stays protected.
func TestCollectLogicalFilePathsUnstattableScopeRootStaysAFailure(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	library := t.TempDir()
	blocked := filepath.Join(library, "Blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "Movie (2001)"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{filepath.Join(blocked, "Movie (2001)")}, "movies", []string{library})
	if err != nil {
		t.Fatalf("collectLogicalFilePaths: %v", err)
	}
	if len(walkFailures) != 1 {
		t.Fatalf("walkFailures = %v, want the unstattable scope", walkFailures)
	}
}

// TestScanSubtreeReconcilesDeletedTitleFolderDB covers a movie folder deleted
// under a reachable library root: the realtime monitor's subtree scan of the
// vanished folder must retire the title like a full library scan does, not
// protect it as unreadable and raise a partial-scan warning.
func TestScanSubtreeReconcilesDeletedTitleFolderDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	folderID := seedDeadRootTestFolder(t, pool, "movies", fmt.Sprintf("vanished-scope-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Paths: []string{root}}
	repo := NewFileRepository(pool)
	s := NewScanner(repo, "", nil, 2, false, time.Hour)

	titleDir := filepath.Join(root, "Harry Test (2001)")
	titleFile := filepath.Join(titleDir, "Harry Test (2001).mkv")
	writeRelinkTestMedia(t, titleFile, 1)
	writeRelinkTestMedia(t, filepath.Join(root, "Keeper (2010)", "Keeper (2010).mkv"), 9)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	contentID := fmt.Sprintf("movie-vanished-scope-%d", folderID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Harry Test', 'matched', '{}'::text[])`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET content_id = $1 WHERE file_path = $2`, contentID, titleFile); err != nil {
		t.Fatalf("link file: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, folderID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	if err := os.RemoveAll(titleDir); err != nil {
		t.Fatalf("remove title folder: %v", err)
	}
	if _, err := s.ScanSubtree(ctx, folder, titleDir); err != nil {
		t.Fatalf("subtree scan: %v", err)
	}

	var missing bool
	if err := pool.QueryRow(ctx, `SELECT missing_since IS NOT NULL FROM media_files WHERE file_path = $1`, titleFile).Scan(&missing); err != nil {
		t.Fatalf("read file row: %v", err)
	}
	if !missing {
		t.Fatal("the deleted folder's file was not marked missing")
	}
	var memberships int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_item_libraries WHERE content_id = $1`, contentID).Scan(&memberships); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 0 {
		t.Fatalf("title still has %d library memberships after its folder was deleted", memberships)
	}
	warning, err := catalog.NewFolderRepository(pool).GetScanWarning(ctx, folderID)
	if err != nil {
		t.Fatalf("read scan warning: %v", err)
	}
	if warning.Code != nil {
		t.Fatalf("scan warning = %q, want none", *warning.Code)
	}
}
