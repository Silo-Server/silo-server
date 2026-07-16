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

	got := deadRootWarningMessage(2, []string{"/mnt/movies"}, nil)
	want := "1 of 2 roots unreachable: /mnt/movies"
	if got != want {
		t.Fatalf("deadRootWarningMessage = %q, want %q", got, want)
	}

	got = deadRootWarningMessage(3, []string{"/a", "/b"}, nil)
	want = "2 of 3 roots unreachable: /a, /b"
	if got != want {
		t.Fatalf("deadRootWarningMessage = %q, want %q", got, want)
	}

	got = deadRootWarningMessage(2, nil, []string{"/mnt/movies"})
	want = "1 of 2 roots returned no files while the library still has cataloged files (lost mount?): /mnt/movies"
	if got != want {
		t.Fatalf("deadRootWarningMessage = %q, want %q", got, want)
	}

	got = deadRootWarningMessage(3, []string{"/a"}, []string{"/b"})
	want = "1 of 3 roots unreachable: /a; 1 of 3 roots returned no files while the library still has cataloged files (lost mount?): /b"
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

func TestScanFolderNestedSuspectEmptyChildRootProtection(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Nested Suspect Root Scan Test")

	base := t.TempDir()
	parent := filepath.Join(base, "media")
	child := filepath.Join(parent, "drive")
	parentFile := filepath.Join(parent, "Alpha (2020)", "Alpha (2020).mkv")
	childFile := filepath.Join(child, "Beta (2021)", "Beta (2021).mkv")
	for _, path := range []string{parentFile, childFile} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	folder := &models.MediaFolder{
		ID: folderID, Paths: []string{parent, child}, Type: "movies",
		Name: "Nested Suspect Root Scan Test", Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 1: %v", err)
	}

	var childID int
	if err := pool.QueryRow(ctx,
		`SELECT id FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, childFile,
	).Scan(&childID); err != nil {
		t.Fatalf("child row after scan 1: %v", err)
	}
	if err := os.RemoveAll(filepath.Dir(childFile)); err != nil {
		t.Fatalf("empty child mountpoint: %v", err)
	}
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("restore empty child mountpoint: %v", err)
	}

	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if len(result.SuspectEmptyRoots) != 1 || result.SuspectEmptyRoots[0] != child {
		t.Fatalf("SuspectEmptyRoots = %v, want [%s]", result.SuspectEmptyRoots, child)
	}
	var gotID int
	var missing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT id, missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, childFile,
	).Scan(&gotID, &missing); err != nil {
		t.Fatalf("child row after scan 2 (was it deleted?): %v", err)
	}
	if gotID != childID || missing == nil {
		t.Fatalf("child row id/missing = %d/%v, want %d/non-nil", gotID, missing, childID)
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

func TestListRootsWithOnlyMissingFiles(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Suspect Root Query Test")

	base := fmt.Sprintf("/drp-suspect-%d", time.Now().UnixNano())
	allMissing := base + "/gone"
	mixed := base + "/mixed"
	empty := base + "/empty"
	stale := time.Now().UTC().Add(-48 * time.Hour)

	seed := func(path string, missing *time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, 1024, $3)
		`, folderID, path, missing); err != nil {
			t.Fatalf("seed media file %s: %v", path, err)
		}
	}
	seed(allMissing+"/Alpha (2020)/Alpha (2020).mkv", &stale)
	seed(allMissing+"/Beta (2021)/Beta (2021).mkv", &stale)
	seed(mixed+"/Gamma (2022)/Gamma (2022).mkv", &stale)
	seed(mixed+"/Delta (2023)/Delta (2023).mkv", nil)

	repo := NewFileRepository(pool)
	got, err := repo.ListRootsWithOnlyMissingFiles(ctx, folderID, []string{allMissing, mixed, empty})
	if err != nil {
		t.Fatalf("ListRootsWithOnlyMissingFiles: %v", err)
	}
	if len(got) != 1 || got[0] != allMissing {
		t.Fatalf("suspect roots = %v, want [%s]", got, allMissing)
	}

	if got, err := repo.ListRootsWithOnlyMissingFiles(ctx, folderID, nil); err != nil || len(got) != 0 {
		t.Fatalf("ListRootsWithOnlyMissingFiles(nil) = %v, %v; want empty", got, err)
	}
}

// TestScanFolderSuspectEmptyRootProtection covers the most common lost-mount
// presentation: the mount drops out but leaves an empty, stat-able mountpoint
// directory behind, so the reachability probe reports the root healthy. The
// walk finds zero files while rows remain cataloged: those rows must only be
// marked missing (surviving a zero-grace sweep), the folder must raise
// dead_root, and a later scan with the operator's one-time cleanup allowance
// armed must complete the deletion and clear the warning.
func TestScanFolderSuspectEmptyRootProtection(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Suspect Empty Root Scan Test")

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
		Name:    "Suspect Empty Root Scan Test",
		Enabled: true,
	}
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

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	idB, _, foundB := fileRow(fileB)
	if !foundB {
		t.Fatal("after scan 1: fileB row not found")
	}

	// Root B's mount drops out, leaving the empty mountpoint directory.
	if err := os.RemoveAll(filepath.Join(rootB, "Beta (2021)")); err != nil {
		t.Fatalf("empty rootB: %v", err)
	}

	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if len(result.UnreachableRoots) != 0 {
		t.Fatalf("scan 2 UnreachableRoots = %v, want empty (root still probes reachable)", result.UnreachableRoots)
	}
	if len(result.SuspectEmptyRoots) != 1 || result.SuspectEmptyRoots[0] != rootB {
		t.Fatalf("scan 2 SuspectEmptyRoots = %v, want [%s]", result.SuspectEmptyRoots, rootB)
	}
	gotIDB, missingB, foundB := fileRow(fileB)
	if !foundB {
		t.Fatal("after scan 2: fileB row was hard-deleted; suspect-empty protection failed")
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

	// Rescan without confirmation: the rows keep surviving.
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 2b: %v", err)
	}
	if _, _, foundB = fileRow(fileB); !foundB {
		t.Fatal("after scan 2b: fileB row was hard-deleted on rescan")
	}

	// The operator confirms the root really is meant to be empty.
	if _, err := pool.Exec(ctx,
		`UPDATE media_folders SET allow_empty_cleanup_once = true WHERE id = $1`, folderID,
	); err != nil {
		t.Fatalf("arm cleanup allowance: %v", err)
	}
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 3: %v", err)
	}
	if _, _, foundB = fileRow(fileB); foundB {
		t.Fatal("after scan 3: fileB row still present; confirmed cleanup did not complete")
	}
	if _, _, foundA := fileRow(fileA); !foundA {
		t.Fatal("after scan 3: fileA row vanished unexpectedly")
	}
	if code, _ := warning(); code != nil {
		t.Fatalf("after scan 3: scan_warning_code = %q, want cleared", *code)
	}
	var allowance bool
	if err := pool.QueryRow(ctx,
		`SELECT allow_empty_cleanup_once FROM media_folders WHERE id = $1`, folderID,
	).Scan(&allowance); err != nil {
		t.Fatalf("query allowance: %v", err)
	}
	if allowance {
		t.Fatal("after scan 3: one-time cleanup allowance was not consumed")
	}
}

// TestScanFolderConfirmedCleanupPreservesDeadRoot pins the confirmed
// empty-cleanup path against dead roots: arming the one-time allowance to
// clean a reachable, intentionally emptied root must not erase a probe-dead
// sibling root's catalog — an outage is never a confirmation.
func TestScanFolderConfirmedCleanupPreservesDeadRoot(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Confirmed Cleanup Dead Root Test")

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
		Name:    "Confirmed Cleanup Dead Root Test",
		Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan 1: %v", err)
	}

	// Root A dies outright; root B is intentionally emptied (dir remains).
	if err := os.RemoveAll(rootA); err != nil {
		t.Fatalf("remove rootA: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(rootB, "Beta (2021)")); err != nil {
		t.Fatalf("empty rootB: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE media_folders SET allow_empty_cleanup_once = true WHERE id = $1`, folderID,
	); err != nil {
		t.Fatalf("arm cleanup allowance: %v", err)
	}

	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if result.EmptyRootGuarded {
		t.Fatal("scan 2 reported EmptyRootGuarded despite the armed allowance")
	}
	if len(result.UnreachableRoots) != 1 || result.UnreachableRoots[0] != rootA {
		t.Fatalf("scan 2 UnreachableRoots = %v, want [%s]", result.UnreachableRoots, rootA)
	}

	var missingA *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, fileA,
	).Scan(&missingA); err != nil {
		t.Fatalf("fileA row after scan 2 (was the dead root's catalog erased?): %v", err)
	}
	if missingA == nil {
		t.Fatal("fileA not marked missing during the outage")
	}
	var countB int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, fileB,
	).Scan(&countB); err != nil {
		t.Fatalf("count fileB rows: %v", err)
	}
	if countB != 0 {
		t.Fatalf("fileB rows = %d, want 0 (confirmed cleanup of the reachable empty root)", countB)
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
		t.Fatalf("scan_warning_code = %v, want dead_root", code)
	}
	if allowance {
		t.Fatal("allowance was not consumed by the confirmed cleanup")
	}
}

// TestSweepMissingAndReconcileProtectsDeadRootsFromScopedScans pins the
// audiobook/ebook/podcast folder-wide sweep against two regressions at once:
// a scoped scan clone (ScanSubtree/ScanFile) whose Paths holds only the
// scanned subtree must still probe every CONFIGURED root (reloaded from the
// DB), and the probe must use the uncompacted list so a nested child mount
// inside a reachable parent is protected independently.
func TestSweepMissingAndReconcileProtectsDeadRootsFromScopedScans(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", "Scoped Sweep Dead Root Test")

	base := t.TempDir()
	parent := filepath.Join(base, "audio")
	child := filepath.Join(parent, "drive")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	for _, p := range []string{parent, child} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`,
			folderID, p,
		); err != nil {
			t.Fatalf("seed folder path %s: %v", p, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_folder_paths WHERE media_folder_id = $1`, folderID)
	})

	stale := time.Now().UTC().Add(-48 * time.Hour)
	seed := func(path string, missing *time.Time) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, missing_since)
			VALUES ($1, $2, 1024, $3) RETURNING id
		`, folderID, path, missing).Scan(&id); err != nil {
			t.Fatalf("seed media file %s: %v", path, err)
		}
		return id
	}
	// A present book keeps the parent root non-suspect, a stale row under the
	// parent is a genuine deletion the sweep must still purge, and a stale row
	// under the (about to die) child mount must survive.
	presentPath := filepath.Join(parent, "Book A", "a.m4b")
	if err := os.MkdirAll(filepath.Dir(presentPath), 0o755); err != nil {
		t.Fatalf("mkdir book: %v", err)
	}
	if err := os.WriteFile(presentPath, []byte("fake audio payload"), 0o644); err != nil {
		t.Fatalf("write book: %v", err)
	}
	seed(presentPath, nil)
	goneID := seed(filepath.Join(parent, "Book B", "b.m4b"), &stale)
	childID := seed(filepath.Join(child, "Book C", "c.m4b"), &stale)

	// The nested child mount dies while the parent stays reachable.
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("remove child mount: %v", err)
	}

	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)
	// A scoped clone the way ScanSubtree/ScanFile build one: Paths is just the
	// scanned subtree, not the configured roots.
	scoped := scopedFolderPaths(&models.MediaFolder{
		ID:      folderID,
		Paths:   []string{parent, child},
		Type:    "audiobooks",
		Name:    "Scoped Sweep Dead Root Test",
		Enabled: true,
	}, []string{filepath.Join(parent, "Book A")})

	if _, _, _, err := scanner.sweepMissingAndReconcile(ctx, scoped, false); err != nil {
		t.Fatalf("sweepMissingAndReconcile: %v", err)
	}

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_files WHERE id = $1)`, childID,
	).Scan(&exists); err != nil {
		t.Fatalf("check child row: %v", err)
	}
	if !exists {
		t.Fatal("row under the dead nested child mount was hard-deleted by a scoped sweep")
	}
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM media_files WHERE id = $1)`, goneID,
	).Scan(&exists); err != nil {
		t.Fatalf("check gone row: %v", err)
	}
	if exists {
		t.Fatal("genuinely deleted row under the reachable parent survived the sweep")
	}
}
