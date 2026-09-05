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

func TestScannedRootRepository_DeleteMissingByFolder(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID1 := seedDeadRootTestFolder(t, pool, "movies", "Scanned Root Repo Test 1")
	folderID2 := seedDeadRootTestFolder(t, pool, "movies", "Scanned Root Repo Test 2")

	repo := NewScannedRootRepository(pool)

	root1 := models.ScannedMediaRoot{
		MediaFolderID: folderID1,
		RootPath:      "/media/movies1/Movie A",
		Title:         "Movie A",
		State:         "matched",
		Year:          2020,
	}
	root2 := models.ScannedMediaRoot{
		MediaFolderID: folderID1,
		RootPath:      "/media/movies2/Movie B", // path that will be removed
		Title:         "Movie B",
		State:         "ambiguous",
		Year:          2021,
	}
	rootOtherFolder := models.ScannedMediaRoot{
		MediaFolderID: folderID2,
		RootPath:      "/media/movies2/Movie B",
		Title:         "Movie B",
		State:         "ambiguous",
		Year:          2021,
	}

	if err := repo.UpsertMany(ctx, []models.ScannedMediaRoot{root1, root2, rootOtherFolder}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	// Scoped delete under /media/movies1 does not touch /media/movies2
	if err := repo.DeleteMissingInScope(ctx, folderID1, "/media/movies1", []string{root1.RootPath}); err != nil {
		t.Fatalf("DeleteMissingInScope: %v", err)
	}
	got, err := repo.Get(ctx, folderID1, root2.RootPath)
	if err != nil {
		t.Fatalf("Get root2: %v", err)
	}
	if got == nil {
		t.Fatalf("root2 was deleted by DeleteMissingInScope for unrelated scope")
	}

	// Full scan delete (only saw root1) removes root2 from folderID1, but leaves folderID2 intact
	if err := repo.DeleteMissingByFolder(ctx, folderID1, []string{root1.RootPath}); err != nil {
		t.Fatalf("DeleteMissingByFolder: %v", err)
	}

	got, err = repo.Get(ctx, folderID1, root2.RootPath)
	if err != nil {
		t.Fatalf("Get root2: %v", err)
	}
	if got != nil {
		t.Fatalf("root2 remained after DeleteMissingByFolder")
	}

	got1, err := repo.Get(ctx, folderID1, root1.RootPath)
	if err != nil || got1 == nil {
		t.Fatalf("root1 was removed unexpectedly: %v", err)
	}

	gotOther, err := repo.Get(ctx, folderID2, rootOtherFolder.RootPath)
	if err != nil || gotOther == nil {
		t.Fatalf("other folder root was removed unexpectedly: %v", err)
	}
}

// TestScanFolderDeadRootProtection walks the real scan pipeline end to end
// with two on-disk roots and verifies the full dead-root story:
//
//  1. a root that disappears leaves its files completely untouched — neither
//     marked missing nor hard-deleted, even with trash emptying enabled and a
//     zero grace (which would delete them in the very same scan without
//     protection) — and the folder surfaces a dead_root scan warning naming
//     the root;
//  2. when the root comes back the rows are still live and the warning clears;
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

	// Scan 2: files under the dead root are left entirely alone — not marked
	// missing, not deleted — and the folder carries a dead_root warning naming
	// the root.
	//
	// Not marking them is the point: catalog reads filter on
	// missing_since IS NULL, so a mark hides the title from browse, search and
	// playback exactly as if it had been deleted. An unreachable root tells us
	// nothing about whether its files exist, so hiding them turns a storage
	// blip into a library outage that persists until the next good scan.
	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if len(result.UnreachableRoots) != 1 || result.UnreachableRoots[0] != rootB {
		t.Fatalf("scan 2 UnreachableRoots = %v, want [%s]", result.UnreachableRoots, rootB)
	}
	if result.MissingSkippedProtected != 1 {
		t.Fatalf("scan 2 MissingSkippedProtected = %d, want 1", result.MissingSkippedProtected)
	}
	if _, missingA, foundA = fileRow(fileA); !foundA || missingA != nil {
		t.Fatalf("after scan 2: fileA found=%v missing=%v, want present and not missing", foundA, missingA)
	}
	gotIDB, missingB, foundB := fileRow(fileB)
	if !foundB {
		t.Fatal("after scan 2: fileB row was hard-deleted; dead-root protection failed")
	}
	if missingB != nil {
		t.Fatalf("after scan 2: fileB marked missing at %v; an unreachable root must not hide its files", missingB)
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

	// Root B returns: the row is still the original, still live, and the
	// warning clears. Because the outage never marked it missing, "recovery"
	// is a no-op on the row rather than an un-hide.
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
// left untouched — neither hidden nor swept — and the folder must warn, even
// though the parent scan is otherwise healthy.
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
	// The parent scan succeeded, but that says nothing about the dead child
	// mount. Its rows must survive untouched — marking them missing on the
	// parent's success would hide the child's whole catalog.
	if childMissing != nil {
		t.Fatalf("child file marked missing at %v after its root died; a dead child mount "+
			"must not be hidden just because its parent root scanned cleanly", childMissing)
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
	// The child mountpoint survived but its contents vanished with the mount,
	// which is indistinguishable from an intentional emptying. The safe
	// reading is to leave the rows visible: hiding them on a guess turns a
	// dropped mount into a library outage, whereas an intentional emptying is
	// confirmed explicitly through the cleanup allowance.
	if gotID != childID {
		t.Fatalf("child row id = %d, want %d", gotID, childID)
	}
	if missing != nil {
		t.Fatalf("child row marked missing at %v; a suspect-empty child mount must not be "+
			"hidden just because its parent root scanned cleanly", missing)
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
	if missing != nil {
		t.Fatalf("file marked missing at %v during an all-roots-dead outage; "+
			"a total outage must leave the catalog intact, not empty the library from users' view", missing)
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
	if missingB != nil {
		t.Fatalf("after scan 2: fileB marked missing at %v; a suspect-empty root "+
			"is a lost mount, so its files must stay visible until the root is confirmed empty", missingB)
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
	if missingA != nil {
		t.Fatalf("fileA marked missing at %v during the outage; confirming cleanup of a "+
			"reachable empty root must not disturb an unrelated unreachable root's files", missingA)
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

// TestScanFolderFlappingRootNeverHidesPresentFiles reproduces the production
// failure this protection exists for: a CephFS-style mount that drops out and
// comes back while its files sit on disk the whole time.
//
// The file is never deleted and never changes. Only the mount flaps. Because
// every catalog read filters on missing_since IS NULL, a single spurious mark
// removes the title from browse, search and playback until the next successful
// scan — users experience it as "this file isn't available anymore" for media
// that is perfectly intact. The row must therefore come through every scan of
// the outage untouched.
func TestScanFolderFlappingRootNeverHidesPresentFiles(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Flapping Root Scan Test")

	base := t.TempDir()
	live := filepath.Join(base, "live")
	flappy := filepath.Join(base, "flappy")
	liveFile := filepath.Join(live, "Alpha (2020)", "Alpha (2020).mkv")
	flappyFile := filepath.Join(flappy, "Beta (2021)", "Beta (2021).mkv")

	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(liveFile)
	write(flappyFile)

	folder := &models.MediaFolder{
		ID:      folderID,
		Paths:   []string{live, flappy},
		Type:    "movies",
		Name:    "Flapping Root Scan Test",
		Enabled: true,
	}
	// Trash emptying on with a zero grace: if a scan ever marks the file
	// missing, the very next scan would also delete the row.
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	missingSince := func(path string) *time.Time {
		t.Helper()
		var missing *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
			folderID, path,
		).Scan(&missing); err != nil {
			t.Fatalf("row for %s went away entirely: %v", path, err)
		}
		return missing
	}

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	if m := missingSince(flappyFile); m != nil {
		t.Fatalf("baseline: file marked missing at %v", m)
	}

	// Simulate the mount dropping and returning repeatedly. The payload on
	// disk is restored byte-for-byte each cycle, exactly as a real mount
	// returning exposes the same inodes.
	stashed := filepath.Join(t.TempDir(), "stash")
	for cycle := range 3 {
		if err := os.Rename(flappy, stashed); err != nil {
			t.Fatalf("cycle %d: drop mount: %v", cycle, err)
		}
		result, err := scanner.ScanFolder(ctx, folder)
		if err != nil {
			t.Fatalf("cycle %d: scan during outage: %v", cycle, err)
		}
		if result.MissingSkippedProtected != 1 {
			t.Fatalf("cycle %d: MissingSkippedProtected = %d, want 1",
				cycle, result.MissingSkippedProtected)
		}
		if m := missingSince(flappyFile); m != nil {
			t.Fatalf("cycle %d: present file hidden at %v during a mount outage", cycle, m)
		}

		if err := os.Rename(stashed, flappy); err != nil {
			t.Fatalf("cycle %d: restore mount: %v", cycle, err)
		}
		if _, err := scanner.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("cycle %d: scan after recovery: %v", cycle, err)
		}
		if m := missingSince(flappyFile); m != nil {
			t.Fatalf("cycle %d: file hidden at %v after the mount returned", cycle, m)
		}
	}

	// The healthy sibling root must be unaffected throughout.
	if m := missingSince(liveFile); m != nil {
		t.Fatalf("file under the always-healthy root marked missing at %v", m)
	}

	// And the genuine-deletion path still works: remove the file for real
	// while its root is reachable, and the row is marked and swept.
	if err := os.Remove(flappyFile); err != nil {
		t.Fatalf("remove flappyFile: %v", err)
	}
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan after real deletion: %v", err)
	}
	var stillThere bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM media_files WHERE media_folder_id = $1 AND file_path = $2)`,
		folderID, flappyFile,
	).Scan(&stillThere); err != nil {
		t.Fatalf("existence check: %v", err)
	}
	if stillThere {
		t.Fatal("a genuinely deleted file under a reachable root survived; real deletions must still be detected")
	}
}

// TestScanFolderFirstScanAfterMountDropsProtectsLiveRows covers Codex review
// finding #2 on PR #472: suspect-empty detection used to require a root whose
// rows were ALL already missing, which meant it could only recognise a lost
// mount one scan too late.
//
// Here the mount drops leaving a reachable but empty mountpoint, and the rows
// are still live because nothing has marked them yet — the state on the very
// first scan after a real mount failure. The root must be classified suspect
// and its rows protected on that first scan, not after they have been hidden.
func TestScanFolderFirstScanAfterMountDropsProtectsLiveRows(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "First Outage Scan Test")

	base := t.TempDir()
	live := filepath.Join(base, "live")
	dropped := filepath.Join(base, "dropped")
	liveFile := filepath.Join(live, "Alpha (2020)", "Alpha (2020).mkv")
	droppedFile := filepath.Join(dropped, "Beta (2021)", "Beta (2021).mkv")

	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write(liveFile)
	write(droppedFile)

	folder := &models.MediaFolder{
		ID: folderID, Paths: []string{live, dropped}, Type: "movies",
		Name: "First Outage Scan Test", Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	var missing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, droppedFile).Scan(&missing); err != nil {
		t.Fatalf("baseline row: %v", err)
	}
	if missing != nil {
		t.Fatalf("baseline: row already missing at %v", missing)
	}

	// The mount drops: contents vanish, the mountpoint directory remains and
	// still probes reachable. The rows under it are all still live.
	if err := os.RemoveAll(filepath.Join(dropped, "Beta (2021)")); err != nil {
		t.Fatalf("empty the dropped root: %v", err)
	}

	result, err := scanner.ScanFolder(ctx, folder)
	if err != nil {
		t.Fatalf("first outage scan: %v", err)
	}
	if len(result.SuspectEmptyRoots) != 1 || result.SuspectEmptyRoots[0] != dropped {
		t.Fatalf("SuspectEmptyRoots = %v, want [%s] on the FIRST scan after the drop",
			result.SuspectEmptyRoots, dropped)
	}
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, droppedFile).Scan(&missing); err != nil {
		t.Fatalf("row after outage scan (hard-deleted?): %v", err)
	}
	if missing != nil {
		t.Fatalf("first scan after the mount dropped hid the row at %v; suspect-empty "+
			"protection must engage before the rows are marked, not after", missing)
	}
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

	files, walkFailures, err := collectLogicalFilePaths(context.Background(), []string{root}, "movies")
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

// TestScanFolderBrokenSymlinkDoesNotFreezeRootReconciliation covers Codex
// review finding #3 on the follow-up commit: walk failures were counted, not
// located, so any failure protected the entire library root.
//
// A dangling symlink is both common and permanent, so that would have
// suppressed missing-file reconciliation for its whole root on every future
// scan — genuinely deleted titles would stay live forever. The failure must be
// scoped to the offending path so the rest of the root still reconciles.
func TestScanFolderBrokenSymlinkDoesNotFreezeRootReconciliation(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Broken Symlink Scan Test")

	root := t.TempDir()
	keeper := filepath.Join(root, "Keeper (2020)", "Keeper (2020).mkv")
	doomed := filepath.Join(root, "Doomed (2021)", "Doomed (2021).mkv")
	for _, p := range []string{keeper, doomed} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// A permanently dangling symlink, the everyday case.
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "dangling.mkv")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	folder := &models.MediaFolder{
		ID: folderID, Paths: []string{root}, Type: "movies",
		Name: "Broken Symlink Scan Test", Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}

	// Genuinely delete one title. The dangling symlink is still there.
	if err := os.RemoveAll(filepath.Dir(doomed)); err != nil {
		t.Fatalf("remove doomed: %v", err)
	}
	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan after deletion: %v", err)
	}

	var missing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, doomed).Scan(&missing); err != nil {
		// The row may already have been swept (zero grace), which also counts
		// as correctly reconciled.
		if !strings.Contains(err.Error(), "no rows") {
			t.Fatalf("doomed row: %v", err)
		}
		missing = nil
	} else if missing == nil {
		t.Fatal("a genuinely deleted title stayed live because an unrelated dangling symlink " +
			"marked the whole root unreconcilable")
	}

	// The surviving title must be untouched throughout.
	var keeperMissing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, keeper).Scan(&keeperMissing); err != nil {
		t.Fatalf("keeper row: %v", err)
	}
	if keeperMissing != nil {
		t.Fatalf("surviving title marked missing at %v", keeperMissing)
	}
}

// TestReprobeNestedRootsCatchesMidScanChildDrop covers Codex review finding #5
// on PR #472: a configured child mount that is healthy at the initial probe
// but gone by the time its compacted parent is walked.
//
// Compaction folds the child into its parent, so it never gets a scope of its
// own, and the post-walk re-probe only revisits scopes that walked empty —
// never a populated parent. Without a re-probe the child's rows are marked
// missing on the parent's success, which is the mid-scan disconnect this
// protection exists for.
func TestReprobeNestedRootsCatchesMidScanChildDrop(t *testing.T) {

	base := t.TempDir()
	parent := filepath.Join(base, "media")
	child := filepath.Join(parent, "child-mount")
	sibling := filepath.Join(base, "unrelated")
	for _, d := range []string{parent, child, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// The child holds media, so it is a live mount rather than a bare
	// mountpoint while it is healthy.
	if err := os.WriteFile(filepath.Join(child, "Alpha (2020).mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed child media: %v", err)
	}
	configured := []string{parent, child, sibling}

	pool := newDeadRootTestPool(t)
	s := &Scanner{fileRepo: NewFileRepository(pool)}

	// Healthy child: nothing to protect.
	unreachable, suspect, err := s.reprobeNestedRoots(context.Background(), 1, configured, parent, false)
	if err != nil {
		t.Fatalf("reprobeNestedRoots (healthy): %v", err)
	}
	if len(unreachable) != 0 || len(suspect) != 0 {
		t.Fatalf("unreachable=%v suspect=%v, want none while the child is reachable", unreachable, suspect)
	}

	// The child mount drops mid-scan.
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("drop child: %v", err)
	}
	unreachable, suspect, err = s.reprobeNestedRoots(context.Background(), 1, configured, parent, false)
	if err != nil {
		t.Fatalf("reprobeNestedRoots (dropped): %v", err)
	}
	// It must land in the UNREACHABLE bucket specifically: reporting a dropped
	// mount as suspect-empty would hand an operator the wrong diagnosis.
	if len(unreachable) != 1 || unreachable[0] != child {
		t.Fatalf("unreachable = %v, want [%s]: a child that drops after the initial probe "+
			"must be caught before its parent's scope is reconciled", unreachable, child)
	}
	if len(suspect) != 0 {
		t.Fatalf("suspect = %v, want none: an unreachable child is not suspect-empty", suspect)
	}

	// Only roots nested under this parent are considered — a sibling root has
	// its own scope and must not be swept in here.
	unreachable, suspect, err = s.reprobeNestedRoots(context.Background(), 1, configured, sibling, false)
	if err != nil {
		t.Fatalf("reprobeNestedRoots (sibling): %v", err)
	}
	if len(unreachable) != 0 || len(suspect) != 0 {
		t.Fatalf("unreachable=%v suspect=%v, want none: %s has no nested configured roots",
			unreachable, suspect, sibling)
	}
}

// TestScanFolderProtectedChildRootSurvivesTrashSweep asserts that a nested
// child root which is offline at scan time keeps its already-missing rows
// through the folder-wide trash sweep, even when those rows are long past the
// removal grace.
//
// Scope note: this stages the child as unreachable BEFORE the scan, so the
// initial probe classifies it and the protection comes from that path. It does
// NOT reproduce the mid-scan drop behind Codex finding #6 — where the child is
// healthy at probe time and dies during the walk, so only reprobeNestedRoots
// sees it. Staging that race needs the drop to land between the probe and the
// walk, which is not reachable from a test without hooks. The fix for that
// path (carrying reprobedRoots into protectedScanRoots) is therefore covered
// by inspection, not by this test; what this test does pin is that the sweep
// honours the protected set it is given.
//
// Trash emptying is on with a zero grace, so any row left unprotected is
// deleted immediately rather than merely hidden.
func TestScanFolderProtectedChildRootSurvivesTrashSweep(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Mid-Scan Drop Sweep Test")

	base := t.TempDir()
	parent := filepath.Join(base, "media")
	child := filepath.Join(parent, "child-mount")
	parentFile := filepath.Join(parent, "Keeper (2020)", "Keeper (2020).mkv")
	childFile := filepath.Join(child, "Child (2021)", "Child (2021).mkv")
	for _, p := range []string{parentFile, childFile} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	folder := &models.MediaFolder{
		ID: folderID, Paths: []string{parent, child}, Type: "movies",
		Name: "Mid-Scan Drop Sweep Test", Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	var childID int
	if err := pool.QueryRow(ctx,
		`SELECT id FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, childFile).Scan(&childID); err != nil {
		t.Fatalf("child row: %v", err)
	}

	// Put the child's row in the state the sweep would delete: already marked
	// missing, well past the (zero) removal grace.
	if _, err := pool.Exec(ctx,
		`UPDATE media_files SET missing_since = NOW() - INTERVAL '48 hours' WHERE id = $1`,
		childID); err != nil {
		t.Fatalf("pre-mark child row: %v", err)
	}

	// The child mount drops. The parent stays healthy and still walks files,
	// so the scan takes the populated-parent path.
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("drop child mount: %v", err)
	}

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan after child drop: %v", err)
	}

	var survives bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM media_files WHERE id = $1)`, childID).Scan(&survives); err != nil {
		t.Fatalf("existence check: %v", err)
	}
	if !survives {
		t.Fatal("row under an offline child root was hard-deleted by the trash sweep; " +
			"an outage must never be a trigger for permanent deletion")
	}

	// The parent's own file must be unaffected throughout.
	var parentMissing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, parentFile).Scan(&parentMissing); err != nil {
		t.Fatalf("parent row: %v", err)
	}
	if parentMissing != nil {
		t.Fatalf("healthy parent file marked missing at %v", parentMissing)
	}
}

// TestScanFolderUnreadableSubtreeSurvivesTrashSweep covers Codex review
// finding #8 on PR #472 — the sibling of finding #6, and the second data-loss
// path in this area.
//
// applyScopedScan protected rows under an unreadable directory locally via
// scope.walkFailures, but the folder-wide protected set was rebuilt without
// those paths. With trash emptying on, DeleteMissingByFolder could then
// permanently delete rows past the removal grace beneath a directory this scan
// could not read — deleting on the strength of an observation never made.
//
// Both that fix and #6's now flow through one accumulated protected set, so
// this test guards the propagation rather than one symptom of losing it.
func TestScanFolderUnreadableSubtreeSurvivesTrashSweep(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny access")
	}
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Unreadable Subtree Sweep Test")

	root := t.TempDir()
	keeper := filepath.Join(root, "Keeper (2020)", "Keeper (2020).mkv")
	hidden := filepath.Join(root, "Locked (2021)", "Locked (2021).mkv")
	for _, p := range []string{keeper, hidden} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	folder := &models.MediaFolder{
		ID: folderID, Paths: []string{root}, Type: "movies",
		Name: "Unreadable Subtree Sweep Test", Enabled: true,
	}
	// Trash emptying on, zero grace: anything left unprotected and already
	// marked missing is deleted on sight.
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	var hiddenID int
	if err := pool.QueryRow(ctx,
		`SELECT id FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, hidden).Scan(&hiddenID); err != nil {
		t.Fatalf("hidden row: %v", err)
	}
	// Put it in the state the sweep would delete.
	if _, err := pool.Exec(ctx,
		`UPDATE media_files SET missing_since = NOW() - INTERVAL '48 hours' WHERE id = $1`,
		hiddenID); err != nil {
		t.Fatalf("pre-mark: %v", err)
	}

	// The subtree becomes unreadable — a permission fault, or storage that
	// stopped answering for part of the tree.
	lockedDir := filepath.Dir(hidden)
	if err := os.Chmod(lockedDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan with unreadable subtree: %v", err)
	}

	var survives bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM media_files WHERE id = $1)`, hiddenID).Scan(&survives); err != nil {
		t.Fatalf("existence check: %v", err)
	}
	if !survives {
		t.Fatal("row beneath an unreadable directory was hard-deleted by the trash sweep; " +
			"a scan must not delete on the strength of an observation it could not make")
	}

	// The readable title is unaffected.
	var keeperMissing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, keeper).Scan(&keeperMissing); err != nil {
		t.Fatalf("keeper row: %v", err)
	}
	if keeperMissing != nil {
		t.Fatalf("readable title marked missing at %v", keeperMissing)
	}
}

// TestScanFolderEmptiedNestedChildUnderEmptyParentStaysVisible pins that a
// nested child whose contents vanish keeps its rows visible, in the awkward
// topology where the child holds the parent's only media and a healthy sibling
// root keeps the folder-wide empty guard from firing.
//
// Scope note, stated because it is easy to misread: the child is emptied
// BEFORE this scan, so the INITIAL probe classifies it as suspect-empty and
// protection arrives through that path. It therefore does NOT exercise the
// pending-scope re-probe added for Codex finding #11, which only matters when
// the child drops AFTER the initial probe. Verified: this test still passes
// with that re-probe disabled.
//
// Staging the real mid-scan race needs the drop to land between the probe and
// the walk, which a test cannot reach without hooks. That fix — like the
// populated-scope re-probe before it — rests on inspection, not on this test.
// What this test does guard is the end-to-end outcome for the topology, which
// no other test covers.
func TestScanFolderEmptiedNestedChildUnderEmptyParentStaysVisible(t *testing.T) {
	pool := newDeadRootTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Pending Empty Parent Test")

	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	child := filepath.Join(parent, "child-mount")
	sibling := filepath.Join(base, "sibling")
	// The parent's ONLY media is inside the child.
	childFile := filepath.Join(child, "Child (2021)", "Child (2021).mkv")
	siblingFile := filepath.Join(sibling, "Sibling (2020)", "Sibling (2020).mkv")
	for _, p := range []string{childFile, siblingFile} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("fake movie payload"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	folder := &models.MediaFolder{
		ID: folderID, Paths: []string{parent, child, sibling}, Type: "movies",
		Name: "Pending Empty Parent Test", Enabled: true,
	}
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, true, 0)

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	var childID int
	if err := pool.QueryRow(ctx,
		`SELECT id FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, childFile).Scan(&childID); err != nil {
		t.Fatalf("child row: %v", err)
	}

	// The child mount's contents vanish but its mountpoint directory remains,
	// so the parent still looks present and non-empty from above.
	if err := os.RemoveAll(filepath.Join(child, "Child (2021)")); err != nil {
		t.Fatalf("empty child mount: %v", err)
	}

	if _, err := scanner.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan after child emptied: %v", err)
	}

	var missing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND id = $2`,
		folderID, childID).Scan(&missing); err != nil {
		t.Fatalf("child row after scan (hard-deleted?): %v", err)
	}
	if missing != nil {
		t.Fatalf("child row hidden at %v: an emptied nested child must stay visible when its "+
			"parent also walks empty and a sibling root keeps the folder guard from firing", missing)
	}

	// The healthy sibling is untouched.
	var siblingMissing *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT missing_since FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folderID, siblingFile).Scan(&siblingMissing); err != nil {
		t.Fatalf("sibling row: %v", err)
	}
	if siblingMissing != nil {
		t.Fatalf("healthy sibling marked missing at %v", siblingMissing)
	}
}
