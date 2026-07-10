package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestDeadRootWarningMessage(t *testing.T) {
	t.Parallel()

	got := deadRootWarningMessage(2, []string{"/mnt/movies"})
	want := "1 of 2 roots unreachable: /mnt/movies"
	if got != want {
		t.Fatalf("deadRootWarningMessage = %q, want %q", got, want)
	}

	got = deadRootWarningMessage(3, []string{"/a", "/b"})
	want = "2 of 3 roots unreachable: /a, /b"
	if got != want {
		t.Fatalf("deadRootWarningMessage = %q, want %q", got, want)
	}
}

func TestProbeUnreachableRoots(t *testing.T) {
	t.Parallel()

	alive := t.TempDir()
	dead := filepath.Join(t.TempDir(), "gone")

	got := probeUnreachableRoots(context.Background(), 1, []string{alive, dead})
	if len(got) != 1 || got[0] != dead {
		t.Fatalf("probeUnreachableRoots = %v, want [%s]", got, dead)
	}
	if got := probeUnreachableRoots(context.Background(), 1, []string{alive}); len(got) != 0 {
		t.Fatalf("probeUnreachableRoots(all alive) = %v, want empty", got)
	}
}

func newDeadRootTestPool(t *testing.T) *pgxpool.Pool {
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

// TestDeleteMissingByFolderProtectedRoots covers the trash-sweep guard at the
// repository level: rows under a protected (unreachable) root survive the
// sweep no matter how stale their missing_since is, sibling roots that merely
// share a string prefix are NOT protected, and an empty protected set
// preserves the historical folder-wide sweep.
func TestDeleteMissingByFolderProtectedRoots(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Dead Root Sweep Test")

	base := fmt.Sprintf("/drp-sweep-%d", time.Now().UnixNano())
	protectedRoot := base + "/movies"
	staleSince := time.Now().UTC().Add(-48 * time.Hour)

	seed := func(path string) int {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, 1024, $3) RETURNING id
		`, folderID, path, staleSince).Scan(&id); err != nil {
			t.Fatalf("seed media file %s: %v", path, err)
		}
		return id
	}
	protectedID := seed(protectedRoot + "/Alpha (2020)/Alpha (2020).mkv")
	seed(base + "/movies2/Beta (2021)/Beta (2021).mkv") // sibling string prefix
	seed(base + "/other/Gamma (2022)/Gamma (2022).mkv")

	repo := NewFileRepository(pool)
	deleted, err := repo.DeleteMissingByFolder(ctx, folderID, 24*time.Hour, []string{protectedRoot})
	if err != nil {
		t.Fatalf("DeleteMissingByFolder with protection: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (sibling-prefix and unrelated rows)", deleted)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM media_files WHERE media_folder_id = $1`, folderID,
	).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining rows = %d, want 1 (the protected row)", remaining)
	}
	var stillThere bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_files WHERE id = $1)`, protectedID,
	).Scan(&stillThere); err != nil {
		t.Fatalf("check protected row: %v", err)
	}
	if !stillThere {
		t.Fatal("protected row was deleted")
	}

	// Without protection the sweep behaves exactly as before and removes it.
	deleted, err = repo.DeleteMissingByFolder(ctx, folderID, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("DeleteMissingByFolder without protection: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
}

// TestScanFolderDeadRootProtection walks the real scan pipeline end to end
// with two on-disk roots and verifies the full dead-root story:
//
//  1. a root that disappears has its files marked missing but never
//     hard-deleted, even with trash emptying enabled and a zero grace
//     (which would delete them in the very same scan without protection),
//     and the folder surfaces a dead_root scan warning naming the root;
//  2. when the root comes back the rows resurrect in place (same ids,
//     missing_since cleared) and the warning clears;
//  3. deleting a file under a reachable root still purges its row after the
//     grace elapses (regression: the historical sweep is untouched).
func TestScanFolderDeadRootProtection(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Dead Root Scan Test")

	base := t.TempDir()
	rootA := filepath.Join(base, "libraryA")
	rootB := filepath.Join(base, "libraryB")
	fileA := filepath.Join(rootA, "Alpha (2020)", "Alpha (2020).mkv")
	fileB := filepath.Join(rootB, "Beta (2021)", "Beta (2021).mkv")
	writeMovie := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeMovie(fileA)
	writeMovie(fileB)

	folder := &models.MediaFolder{
		ID:      folderID,
		Paths:   []string{rootA, rootB},
		Type:    "movies",
		Name:    "Dead Root Scan Test",
		Enabled: true,
	}

	// emptyTrashAfterScan=true with zero grace: a missing row is eligible for
	// deletion in the very scan that marks it missing.
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	fileRow := func(path string) (id int, missingSince *time.Time, found bool) {
		t.Helper()
		err := pool.QueryRow(ctx,
			`SELECT id, missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
			folderID, path,
		).Scan(&id, &missingSince)
		if err != nil {
			if strings.Contains(err.Error(), "no rows") {
				return 0, nil, false
			}
			t.Fatalf("query file row %s: %v", path, err)
		}
		return id, missingSince, true
	}
	warning := func() (code, message *string) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`,
			folderID,
		).Scan(&code, &message); err != nil {
			t.Fatalf("query scan warning: %v", err)
		}
		return code, message
	}

	// Scan 1: both roots healthy.
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	idA, missingA, foundA := fileRow(fileA)
	idB, missingB, foundB := fileRow(fileB)
	if !foundA || !foundB {
		t.Fatalf("after scan 1: foundA=%v foundB=%v, want both rows", foundA, foundB)
	}
	if missingA != nil || missingB != nil {
		t.Fatalf("after scan 1: missingA=%v missingB=%v, want both nil", missingA, missingB)
	}

	// Root B dies (unmounted / dead drive).
	if err := os.RemoveAll(rootB); err != nil {
		t.Fatalf("remove rootB: %v", err)
	}

	// Scan 2: files under the dead root are marked missing (hidden) but the
	// row must survive the sweep, and the folder must carry a dead_root
	// warning naming the root.
	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if len(result.UnreachableRoots) != 1 || result.UnreachableRoots[0] != rootB {
		t.Fatalf("scan 2 UnreachableRoots = %v, want [%s]", result.UnreachableRoots, rootB)
	}
	if _, missingA, foundA = fileRow(fileA); !foundA || missingA != nil {
		t.Fatalf("after scan 2: fileA found=%v missing=%v, want present and not missing", foundA, missingA)
	}
	gotIDB, missingB, foundB := fileRow(fileB)
	if !foundB {
		t.Fatal("after scan 2: fileB row was hard-deleted; dead-root protection failed")
	}
	if missingB == nil {
		t.Fatal("after scan 2: fileB not marked missing; it should be hidden")
	}
	if gotIDB != idB {
		t.Fatalf("after scan 2: fileB id changed %d -> %d", idB, gotIDB)
	}
	code, message := warning()
	if code == nil || *code != "dead_root" {
		t.Fatalf("after scan 2: scan_warning_code = %v, want dead_root", code)
	}
	if message == nil || !strings.Contains(*message, rootB) {
		t.Fatalf("after scan 2: scan_warning_message = %v, want to contain %q", message, rootB)
	}

	// Rescan while still dead: row keeps surviving (grace long since elapsed).
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 2b: %v", err)
	}
	if _, _, foundB = fileRow(fileB); !foundB {
		t.Fatal("after scan 2b: fileB row was hard-deleted on rescan")
	}

	// Root B returns: the same row resurrects (same id, missing cleared) and
	// the warning clears.
	writeMovie(fileB)
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 3: %v", err)
	}
	gotIDB, missingB, foundB = fileRow(fileB)
	if !foundB || missingB != nil {
		t.Fatalf("after scan 3: fileB found=%v missing=%v, want resurrected", foundB, missingB)
	}
	if gotIDB != idB {
		t.Fatalf("after scan 3: fileB resurrected under a new id %d, want original %d", gotIDB, idB)
	}
	if code, _ := warning(); code != nil {
		t.Fatalf("after scan 3: scan_warning_code = %q, want cleared", *code)
	}
	if _, missingA, _ = fileRow(fileA); missingA != nil {
		t.Fatalf("after scan 3: fileA missing = %v, want nil", missingA)
	}
	_ = idA

	// Regression: deleting one FILE under a reachable root still purges its
	// row once the grace (zero here) elapses — reachable-root semantics are
	// unchanged.
	if err := os.Remove(fileB); err != nil {
		t.Fatalf("remove fileB: %v", err)
	}
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 4: %v", err)
	}
	if _, _, foundB = fileRow(fileB); foundB {
		t.Fatal("after scan 4: fileB row still present; reachable-root purge regressed")
	}
	if _, _, foundA = fileRow(fileA); !foundA {
		t.Fatal("after scan 4: fileA row vanished unexpectedly")
	}
	if code, _ := warning(); code != nil {
		t.Fatalf("after scan 4: scan_warning_code = %q, want none", *code)
	}
}

// TestScanFolderNestedDeadChildRootProtection covers a child mount configured
// INSIDE a reachable parent root (/parent plus /parent/child). Traversal
// compaction drops the child, but it can die independently: its files must be
// protected from the sweep and the folder must warn, even though the parent
// scan is otherwise healthy.
func TestScanFolderNestedDeadChildRootProtection(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Nested Dead Root Scan Test")

	base := t.TempDir()
	parent := filepath.Join(base, "media")
	child := filepath.Join(parent, "drive")
	fileParent := filepath.Join(parent, "Alpha (2020)", "Alpha (2020).mkv")
	fileChild := filepath.Join(child, "Beta (2021)", "Beta (2021).mkv")
	writeMovie := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeMovie(fileParent)
	writeMovie(fileChild)

	folder := &models.MediaFolder{
		ID:      folderID,
		Paths:   []string{parent, child},
		Type:    "movies",
		Name:    "Nested Dead Root Scan Test",
		Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	var childID int
	var childMissing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT id, missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, fileChild,
	).Scan(&childID, &childMissing); err != nil {
		t.Fatalf("child row after scan 1: %v", err)
	}
	if childMissing != nil {
		t.Fatalf("child missing after scan 1: %v, want nil", childMissing)
	}

	// The child mount dies while the parent stays reachable. Compaction hides
	// the child from traversal, so only the uncompacted probe can protect it.
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("remove child root: %v", err)
	}
	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if len(result.UnreachableRoots) != 1 || result.UnreachableRoots[0] != child {
		t.Fatalf("scan 2 UnreachableRoots = %v, want [%s]", result.UnreachableRoots, child)
	}
	var gotID int
	if err := pool.QueryRow(ctx,
		`SELECT id, missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, fileChild,
	).Scan(&gotID, &childMissing); err != nil {
		t.Fatalf("child row after scan 2 (was it hard-deleted?): %v", err)
	}
	if childMissing == nil {
		t.Fatal("child file not marked missing after its root died")
	}
	if gotID != childID {
		t.Fatalf("child row id changed %d -> %d", childID, gotID)
	}
	var code, message *string
	if err := pool.QueryRow(ctx,
		`SELECT scan_warning_code, scan_warning_message FROM media_folders WHERE id = $1`,
		folderID,
	).Scan(&code, &message); err != nil {
		t.Fatalf("query warning: %v", err)
	}
	if code == nil || *code != "dead_root" {
		t.Fatalf("scan_warning_code = %v, want dead_root", code)
	}
	if message == nil || !strings.Contains(*message, child) {
		t.Fatalf("scan_warning_message = %v, want to contain %q", message, child)
	}
}

// TestScanFolderAllRootsDeadOutage covers the single-drive-library outage:
// when EVERY configured root is unreachable, the scan must bypass the
// empty-root confirm flow (without consuming the operator's one-time cleanup
// allowance), mark all files missing so they hide, keep every row, and raise
// dead_root — not empty_root.
func TestScanFolderAllRootsDeadOutage(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "All Roots Dead Scan Test")

	base := t.TempDir()
	root := filepath.Join(base, "movies")
	file := filepath.Join(root, "Alpha (2020)", "Alpha (2020).mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(file, []byte("fake movie payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	folder := &models.MediaFolder{
		ID:      folderID,
		Paths:   []string{root},
		Type:    "movies",
		Name:    "All Roots Dead Scan Test",
		Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 1: %v", err)
	}

	// Arm the one-time cleanup allowance so we can prove the outage path does
	// NOT consume it (it must stay reserved for a deliberate empty-root scan).
	if _, err := pool.Exec(ctx,
		`UPDATE media_folders SET allow_empty_cleanup_once = true WHERE id = $1`, folderID,
	); err != nil {
		t.Fatalf("arm cleanup allowance: %v", err)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if result.EmptyRootGuarded {
		t.Fatal("scan 2 reported EmptyRootGuarded; all-dead outage should take the dead_root path")
	}

	var missing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, file,
	).Scan(&missing); err != nil {
		t.Fatalf("file row after scan 2 (was it hard-deleted?): %v", err)
	}
	if missing == nil {
		t.Fatal("file not marked missing during all-roots-dead outage")
	}

	var code *string
	var allowance bool
	if err := pool.QueryRow(ctx,
		`SELECT scan_warning_code, allow_empty_cleanup_once FROM media_folders WHERE id = $1`,
		folderID,
	).Scan(&code, &allowance); err != nil {
		t.Fatalf("query folder state: %v", err)
	}
	if code == nil || *code != "dead_root" {
		t.Fatalf("scan_warning_code = %v, want dead_root (not empty_root)", code)
	}
	if !allowance {
		t.Fatal("outage scan consumed the empty-cleanup allowance; it must be preserved")
	}
}
