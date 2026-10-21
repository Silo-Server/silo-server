package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
