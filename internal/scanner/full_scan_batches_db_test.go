package scanner

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// batchedScanLibrary is one copy of a test library and the scanner that scans
// it with a given batch size.
type batchedScanLibrary struct {
	t       *testing.T
	pool    *pgxpool.Pool
	scanner *Scanner
	folder  *models.MediaFolder
	root    string
}

// batchedScanFiles is the library, in walk order. Alpha is one show split
// across two top-level folders by its provider tag; with two files to a batch
// both Alpha and Beta have their files in more than one batch. Every folder
// names its title: a folder that does not gets a group key derived from its
// absolute path, which two copies of the library would not share.
var batchedScanFiles = []string{
	"Alpha (2019) [tvdb-81189]/Season 01/Alpha - S01E01.mkv",
	"Alpha (2019) [tvdb-81189]/Season 01/Alpha - S01E02.mkv",
	"Alpha S2 [tvdb-81189]/Season 02/Alpha - S02E01.mkv",
	"Alpha S2 [tvdb-81189]/Season 02/Alpha - S02E02.mkv",
	"Beta/Season 01/Beta - S01E01.mkv",
	"Beta/Season 01/Beta - S01E02.mkv",
	"Beta/Season 01/Beta - S01E03.mkv",
	"Gamma/Season 01/Gamma - S01E01.mkv",
}

func newBatchedScanLibrary(t *testing.T, pool *pgxpool.Pool, folderType string, files []string, batchSize int) *batchedScanLibrary {
	t.Helper()
	root := t.TempDir()
	for i, rel := range files {
		// A different seed per file keeps one from passing for a moved copy
		// of another.
		writeRelinkTestMedia(t, filepath.Join(root, rel), byte(i+1))
	}
	folderID := seedDeadRootTestFolder(t, pool, folderType, fmt.Sprintf("batched-scan-%d-%d", batchSize, time.Now().UnixNano()))
	scanner := NewScanner(NewFileRepository(pool), "", nil, 2, false, time.Hour)
	scanner.scanBatchSize = batchSize
	return &batchedScanLibrary{
		t:       t,
		pool:    pool,
		scanner: scanner,
		folder:  &models.MediaFolder{ID: folderID, Type: folderType, Paths: []string{root}, Enabled: true},
		root:    root,
	}
}

func (l *batchedScanLibrary) scan(step string) *ScanResult {
	l.t.Helper()
	result, err := l.scanner.ScanFolder(l.t.Context(), l.folder)
	if err != nil {
		l.t.Fatalf("%s: scan: %v", step, err)
	}
	if result.Errors != 0 {
		l.t.Fatalf("%s: scan reported %d file errors", step, result.Errors)
	}
	return result
}

// lines returns the rows of a one-column text query whose only argument is the
// folder id, with the library root taken out of them.
func (l *batchedScanLibrary) lines(step, query string) []string {
	l.t.Helper()
	rows, err := l.pool.Query(l.t.Context(), query, l.folder.ID)
	if err != nil {
		l.t.Fatalf("%s: read catalog: %v", step, err)
	}
	defer rows.Close()
	lines := make([]string, 0)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			l.t.Fatalf("%s: scan catalog row: %v", step, err)
		}
		lines = append(lines, strings.ReplaceAll(line, l.root, "<root>"))
	}
	if err := rows.Err(); err != nil {
		l.t.Fatalf("%s: iterate catalog: %v", step, err)
	}
	return lines
}

// snapshot returns what the scans have written for the library, with the
// library root, row ids and timestamps taken out so that two copies of the
// library compare equal when they were cataloged the same way.
func (l *batchedScanLibrary) snapshot(step string) []string {
	l.t.Helper()
	lines := make([]string, 0)
	for _, query := range []string{
		`SELECT 'file ' || jsonb_build_object(
				'path', file_path, 'missing', missing_since IS NOT NULL, 'size', file_size,
				'group', content_group_key, 'group_version', group_key_version,
				'observed_root', observed_root_path, 'canonical_root', canonical_root_path,
				'title', base_title, 'year', base_year, 'type', base_type,
				'confidence', identity_confidence, 'identity', identity_json,
				'multi_start', multi_episode_start, 'multi_end', multi_episode_end)::text
			FROM media_files WHERE media_folder_id = $1`,
		`SELECT 'root ' || (to_jsonb(t) - 'media_folder_id' - 'first_seen_at' - 'last_seen_at')::text
			FROM scanned_media_roots t WHERE media_folder_id = $1`,
		`SELECT 'group ' || (to_jsonb(t) - 'media_folder_id' - 'first_seen_at' - 'last_seen_at')::text
			FROM scanned_media_groups t WHERE media_folder_id = $1`,
		`SELECT 'location ' || (to_jsonb(t) - 'media_folder_id' - 'first_seen_at' - 'last_seen_at')::text
			FROM observed_media_locations t WHERE media_folder_id = $1`,
		`SELECT 'group location ' || (to_jsonb(t) - 'media_folder_id' - 'first_seen_at' - 'last_seen_at')::text
			FROM media_group_locations t WHERE media_folder_id = $1`,
	} {
		lines = append(lines, l.lines(step, query)...)
	}
	slices.Sort(lines)
	return lines
}

// count runs a count query whose first argument is the folder id.
func (l *batchedScanLibrary) count(step, query string, args ...any) int {
	l.t.Helper()
	var n int
	if err := l.pool.QueryRow(l.t.Context(), query, append([]any{l.folder.ID}, args...)...).Scan(&n); err != nil {
		l.t.Fatalf("%s: count: %v", step, err)
	}
	return n
}

// missingUnder counts the files under a directory of the library that are
// marked missing, and the ones that are not.
func (l *batchedScanLibrary) missingUnder(step, dir string) (missing, present int) {
	l.t.Helper()
	prefix := filepath.Join(l.root, dir) + string(filepath.Separator)
	missing = l.count(step, `SELECT count(*) FROM media_files
		WHERE media_folder_id = $1 AND starts_with(file_path, $2) AND missing_since IS NOT NULL`, prefix)
	present = l.count(step, `SELECT count(*) FROM media_files
		WHERE media_folder_id = $1 AND starts_with(file_path, $2) AND missing_since IS NULL`, prefix)
	return missing, present
}

func (l *batchedScanLibrary) fileIDs(step string) map[string]int {
	l.t.Helper()
	rows, err := l.pool.Query(l.t.Context(), `SELECT file_path, id FROM media_files WHERE media_folder_id = $1`, l.folder.ID)
	if err != nil {
		l.t.Fatalf("%s: read file ids: %v", step, err)
	}
	defer rows.Close()
	ids := make(map[string]int)
	for rows.Next() {
		var path string
		var id int
		if err := rows.Scan(&path, &id); err != nil {
			l.t.Fatalf("%s: scan file id: %v", step, err)
		}
		ids[path] = id
	}
	if err := rows.Err(); err != nil {
		l.t.Fatalf("%s: iterate file ids: %v", step, err)
	}
	return ids
}

// batchedScanPair is two copies of the library: one scanned two files to a
// batch, one in a single batch.
type batchedScanPair struct {
	t       *testing.T
	batched *batchedScanLibrary
	single  *batchedScanLibrary
}

func (p batchedScanPair) each(visit func(*batchedScanLibrary)) {
	visit(p.batched)
	visit(p.single)
}

func (p batchedScanPair) compare(step string) {
	p.t.Helper()
	got, want := p.batched.snapshot(step), p.single.snapshot(step)
	if !slices.Equal(got, want) {
		p.t.Fatalf("%s: a batched scan cataloged the library differently\nbatched:\n%s\nsingle batch:\n%s",
			step, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestFullScanInBatchesCatalogsLikeOneBatchDB scans two copies of a library,
// one with two files to a batch and one in a single batch, through a first
// scan, a rescan, a deleted top-level folder, an unreadable folder and a
// dropped mount. A show's files, a content group and a vanished folder all
// cross batch boundaries in the first copy, and after every step both copies
// must be cataloged the same way.
func TestFullScanInBatchesCatalogsLikeOneBatchDB(t *testing.T) {
	pool := newScannerTestPool(t)
	pair := batchedScanPair{
		t:       t,
		batched: newBatchedScanLibrary(t, pool, "series", batchedScanFiles, 2),
		single:  newBatchedScanLibrary(t, pool, "series", batchedScanFiles, 0),
	}
	checkBatchedFirstScan(t, pair)
	checkBatchedRescan(t, pair)
	checkBatchedDeletedFolder(t, pair)
	// Permission bits do not stop root.
	if os.Geteuid() != 0 {
		checkBatchedUnreadableFolder(t, pair)
	}
	checkBatchedDroppedMount(t, pair)
}

func checkBatchedFirstScan(t *testing.T, pair batchedScanPair) {
	t.Helper()
	const step = "first scan"
	pair.each(func(library *batchedScanLibrary) {
		if result := library.scan(step); result.New != len(batchedScanFiles) {
			t.Fatalf("%s: New = %d, want %d", step, result.New, len(batchedScanFiles))
		}
	})
	pair.compare(step)

	// The show split across two top-level folders is one group holding all
	// four files, with the first folder walked as its primary location.
	batched := pair.batched
	if n := batched.count(step, `SELECT observed_file_count FROM scanned_media_groups
		WHERE media_folder_id = $1 AND tvdb_id = '81189'`); n != 4 {
		t.Fatalf("%s: split show's group counts %d files, want 4", step, n)
	}
	const splitShowLocations = `SELECT count(*) FROM media_group_locations l
		JOIN scanned_media_groups g USING (media_folder_id, group_key_version, content_group_key)
		WHERE l.media_folder_id = $1 AND g.tvdb_id = '81189'`
	if n := batched.count(step, splitShowLocations); n != 2 {
		t.Fatalf("%s: split show's group has %d locations, want 2", step, n)
	}
	if n := batched.count(step, splitShowLocations+` AND l.is_primary`); n != 1 {
		t.Fatalf("%s: split show's group has %d primary locations, want 1", step, n)
	}
	if n := batched.count(step, splitShowLocations+` AND l.is_primary AND l.observed_root_path = $2`,
		filepath.Join(batched.root, "Alpha (2019) [tvdb-81189]")); n != 1 {
		t.Fatalf("%s: the first folder walked is not the split show's primary location", step)
	}
	if n := batched.count(step, `SELECT observed_file_count FROM scanned_media_roots
		WHERE media_folder_id = $1 AND root_path = $2`, filepath.Join(batched.root, "Beta")); n != 3 {
		t.Fatalf("%s: Beta's root counts %d files, want 3", step, n)
	}
}

// A rescan reads each batch's cataloged rows by path. A row it failed to find
// would be taken for a new file.
func checkBatchedRescan(t *testing.T, pair batchedScanPair) {
	t.Helper()
	const step = "rescan"
	idsBefore := pair.batched.fileIDs(step)
	pair.each(func(library *batchedScanLibrary) {
		if result := library.scan(step); result.New != 0 || result.Missing != 0 {
			t.Fatalf("%s: New = %d, Missing = %d, want 0 and 0", step, result.New, result.Missing)
		}
	})
	pair.compare(step)
	if idsAfter := pair.batched.fileIDs(step); len(idsAfter) != len(batchedScanFiles) || !maps.Equal(idsBefore, idsAfter) {
		t.Fatalf("%s: file rows changed: before %v, after %v", step, idsBefore, idsAfter)
	}
}

// A whole top-level folder deleted from disk has no files left to put in any
// batch; its cataloged files must still be marked missing and its snapshot
// pruned.
func checkBatchedDeletedFolder(t *testing.T, pair batchedScanPair) {
	t.Helper()
	const step = "deleted top-level folder"
	pair.each(func(library *batchedScanLibrary) {
		if err := os.RemoveAll(filepath.Join(library.root, "Beta")); err != nil {
			t.Fatalf("%s: remove folder: %v", step, err)
		}
		library.scan(step)
	})
	pair.compare(step)

	batched := pair.batched
	if missing, present := batched.missingUnder(step, "Beta"); missing != 3 || present != 0 {
		t.Fatalf("%s: Beta has %d missing and %d present files, want 3 and 0", step, missing, present)
	}
	if missing, present := batched.missingUnder(step, ""); missing != 3 || present != 5 {
		t.Fatalf("%s: library has %d missing and %d present files, want 3 and 5", step, missing, present)
	}
	if n := batched.count(step, `SELECT count(*) FROM scanned_media_roots
		WHERE media_folder_id = $1 AND root_path = $2`, filepath.Join(batched.root, "Beta")); n != 0 {
		t.Fatalf("%s: the deleted folder's root snapshot was not pruned", step)
	}
}

// A folder the walk cannot read is not an inventory: its files stay, and the
// scan says it was partial.
func checkBatchedUnreadableFolder(t *testing.T, pair batchedScanPair) {
	t.Helper()
	const step = "unreadable folder"
	pair.each(func(library *batchedScanLibrary) {
		blocked := filepath.Join(library.root, "Gamma")
		if err := os.Chmod(blocked, 0o000); err != nil {
			t.Fatalf("%s: chmod: %v", step, err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
		library.scan(step)
		if err := os.Chmod(blocked, 0o755); err != nil {
			t.Fatalf("%s: restore permissions: %v", step, err)
		}
	})
	pair.compare(step)

	batched := pair.batched
	if missing, present := batched.missingUnder(step, "Gamma"); missing != 0 || present != 1 {
		t.Fatalf("%s: Gamma has %d missing and %d present files, want 0 and 1", step, missing, present)
	}
	warning, err := catalog.NewFolderRepository(batched.pool).GetScanWarning(t.Context(), batched.folder.ID)
	if err != nil {
		t.Fatalf("%s: read scan warning: %v", step, err)
	}
	if warning.Code == nil || *warning.Code != partialWalkWarningCode {
		t.Fatalf("%s: scan warning = %v, want %q", step, warning.Code, partialWalkWarningCode)
	}
}

// A root that walks empty while files are still cataloged under it looks like
// a dropped mount: nothing may be marked missing.
func checkBatchedDroppedMount(t *testing.T, pair batchedScanPair) {
	t.Helper()
	const step = "dropped mount"
	pair.each(func(library *batchedScanLibrary) {
		entries, err := os.ReadDir(library.root)
		if err != nil {
			t.Fatalf("%s: list root: %v", step, err)
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(library.root, entry.Name())); err != nil {
				t.Fatalf("%s: empty root: %v", step, err)
			}
		}
		if result := library.scan(step); !result.EmptyRootGuarded {
			t.Fatalf("%s: the empty root was not guarded", step)
		}
	})
	pair.compare(step)
	if missing, present := pair.batched.missingUnder(step, ""); missing != 3 || present != 5 {
		t.Fatalf("%s: library has %d missing and %d present files, want 3 and 5", step, missing, present)
	}
}

// TestFullScanInBatchesFailedReadReconcilesNothingDB covers a batch whose
// cataloged rows cannot be read after earlier batches were processed. The scan
// must fail without reconciling: a file gone from disk is not marked missing,
// and no root, group or location snapshot is rewritten from the partial walk.
func TestFullScanInBatchesFailedReadReconcilesNothingDB(t *testing.T) {
	pool := newScannerTestPool(t)
	library := newBatchedScanLibrary(t, pool, "series", batchedScanFiles, 1)
	library.scan("first scan")
	before := library.snapshot("first scan")

	// The first file walked is gone, and the row of the last one makes its
	// batch's read fail.
	if err := os.Remove(filepath.Join(library.root, batchedScanFiles[0])); err != nil {
		t.Fatalf("remove a file: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET external_subtitles = '{}'::jsonb WHERE file_path = $1`,
		filepath.Join(library.root, batchedScanFiles[len(batchedScanFiles)-1])); err != nil {
		t.Fatalf("break a row: %v", err)
	}
	if _, err := library.scanner.ScanFolder(t.Context(), library.folder); err == nil {
		t.Fatal("scan succeeded, want the failed batch read reported")
	}
	if after := library.snapshot("failed scan"); !slices.Equal(before, after) {
		t.Fatalf("failed scan changed the catalog:\nbefore %q\nafter  %q", before, after)
	}
}

// TestFullScanInBatchesFindsCatalogedExtrasDB covers the extras of a batched
// scan, whose cataloged rows are read by path after the primary files. Once
// the movie has an item its extras are cataloged; the scan after that must
// find their rows instead of taking them for new files.
func TestFullScanInBatchesFindsCatalogedExtrasDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	library := newBatchedScanLibrary(t, pool, "movies", []string{
		"Film (2001)/Extras/Interview.mkv",
		"Film (2001)/Extras/Making Of.mkv",
		"Film (2001)/Film (2001).mkv",
		"Keeper (2010)/Keeper (2010).mkv",
	}, 1)

	// The movie has no item yet, so its extras wait.
	if result := library.scan("first scan"); result.New != 2 {
		t.Fatalf("first scan: New = %d, want the 2 movies", result.New)
	}
	contentID := fmt.Sprintf("movie-batched-extras-%d", library.folder.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Film', 'matched', '{}'::text[])`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET content_id = $1 WHERE file_path = $2`,
		contentID, filepath.Join(library.root, "Film (2001)", "Film (2001).mkv")); err != nil {
		t.Fatalf("link file: %v", err)
	}

	if result := library.scan("scan with a parent"); result.New != 2 {
		t.Fatalf("scan with a parent: New = %d, want the 2 extras", result.New)
	}
	idsBefore := library.fileIDs("scan with a parent")
	if result := library.scan("rescan"); result.New != 0 {
		t.Fatalf("rescan: New = %d, want 0: the extras' rows were not found", result.New)
	}
	if idsAfter := library.fileIDs("rescan"); len(idsAfter) != 4 || !maps.Equal(idsBefore, idsAfter) {
		t.Fatalf("rescan: file rows changed: before %v, after %v", idsBefore, idsAfter)
	}
}
