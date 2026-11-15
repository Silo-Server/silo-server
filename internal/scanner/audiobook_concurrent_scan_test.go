package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// audiobookSkipCheckSQL matches the skip check, a scan's first read after its
// walk (ListByObservedRootPath).
const audiobookSkipCheckSQL = "AND observed_root_path = $2"

// queryOnceHook runs fn once, before the first query whose SQL contains match.
type queryOnceHook struct {
	match string
	once  sync.Once
	fn    func()
}

func (h *queryOnceHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, h.match) {
		h.once.Do(h.fn)
	}
	return ctx
}

func (*queryOnceHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// concurrentBookFixture is an audiobook library holding one book folder,
// "Copied Author", for tests that run two scans of it at once.
type concurrentBookFixture struct {
	pool    *pgxpool.Pool
	ffprobe string
	root    string
	bookDir string
	folder  *models.MediaFolder
}

func newConcurrentBookFixture(t *testing.T) *concurrentBookFixture {
	t.Helper()
	pool := newScannerTestPool(t)
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobe, `#!/bin/sh
cat <<'JSON'
{"format":{"format_name":"mp3","duration":"1800.0","size":"1","bit_rate":"64000","tags":{"album":"Copied Book","artist":"Copied Author"}},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","channels":2}],"chapters":[]}
JSON
`)
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", fmt.Sprintf("concurrent-book-%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id = $1)`, folderID)
	})
	root := t.TempDir()
	f := &concurrentBookFixture{
		pool:    pool,
		ffprobe: ffprobe,
		root:    root,
		bookDir: filepath.Join(root, "Copied Author"),
		folder:  &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}},
	}
	if err := os.MkdirAll(f.bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// alias links Alias to the book folder and returns the link's path.
func (f *concurrentBookFixture) alias(t *testing.T) string {
	t.Helper()
	alias := filepath.Join(f.root, "Alias")
	if err := os.Symlink(f.bookDir, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func (f *concurrentBookFixture) copyPart(name string) error {
	return os.WriteFile(filepath.Join(f.bookDir, name), []byte(name), 0o644)
}

// hookedScanner is newHookedScanner with the fixture's fake ffprobe.
func (f *concurrentBookFixture) hookedScanner(t *testing.T, match string, fn func()) *Scanner {
	t.Helper()
	return newHookedScanner(t, f.ffprobe, match, fn)
}

// newHookedScanner returns a scanner on its own pool that runs fn before its
// first query containing match. It stands in for a scan on any node.
func newHookedScanner(t *testing.T, ffprobe, match string, fn func()) *Scanner {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.Tracer = &queryOnceHook{match: match, fn: fn}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect hooked pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewScanner(NewFileRepository(pool), ffprobe, nil, 2, false, time.Hour)
}

// requireParts compares the library's present files, relative to its root,
// with their presentation kind and part numbering.
func (f *concurrentBookFixture) requireParts(t *testing.T, want ...string) {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `
		SELECT file_path, presentation_kind, COALESCE(presentation_part_index, 0), COALESCE(presentation_part_total, 0)
		FROM media_files WHERE media_folder_id = $1 AND missing_since IS NULL ORDER BY file_path`, f.folder.ID)
	if err != nil {
		t.Fatalf("list present files: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var path, kind string
		var index, total int
		if err := rows.Scan(&path, &kind, &index, &total); err != nil {
			t.Fatalf("scan present file: %v", err)
		}
		rel, _ := filepath.Rel(f.root, path)
		got = append(got, fmt.Sprintf("%s %s %d/%d", rel, kind, index, total))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list present files: %v", err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("present files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestScanFileAudiobookKeepsPartsIndexedByConcurrentScanDB covers two scans of
// one book while its parts are copied in. The first scan walks the book before
// the second part exists; another scan then indexes both parts, through the
// same folder or through a symlinked alias of it. The first scan must neither
// mark the new part missing nor rewrite the book without it.
func TestScanFileAudiobookKeepsPartsIndexedByConcurrentScanDB(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alias bool
	}{
		{name: "same folder"},
		{name: "symlinked alias", alias: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConcurrentBookFixture(t)
			otherDir := f.bookDir
			if tc.alias {
				otherDir = f.alias(t)
			}
			if err := f.copyPart("01.mp3"); err != nil {
				t.Fatal(err)
			}
			other := NewScanner(NewFileRepository(f.pool), f.ffprobe, nil, 2, false, time.Hour)
			var otherErr error
			ran := false
			s := f.hookedScanner(t, audiobookSkipCheckSQL, func() {
				ran = true
				if otherErr = f.copyPart("02.mp3"); otherErr == nil {
					otherErr = other.ScanFile(context.Background(), filepath.Join(otherDir, "02.mp3"), f.folder)
				}
			})

			if err := s.ScanFile(t.Context(), filepath.Join(f.bookDir, "01.mp3"), f.folder); err != nil {
				t.Fatalf("ScanFile: %v", err)
			}
			if !ran {
				t.Fatal("the other scan never ran; the skip-check query was not traced")
			}
			if otherErr != nil {
				t.Fatalf("other scan: %v", otherErr)
			}
			otherRel, _ := filepath.Rel(f.root, otherDir)
			f.requireParts(t,
				filepath.Join(otherRel, "01.mp3")+" "+audiobookMultipartKind+" 1/2",
				filepath.Join(otherRel, "02.mp3")+" "+audiobookMultipartKind+" 2/2",
			)
		})
	}
}

// TestScanFileAudiobookNewerAliasInventoryWinsDB covers the reverse order
// through a symlinked alias: an older scan of the alias walks the book before
// its third part arrives and publishes first, then a scan of the book folder
// that walked all three parts publishes. The newer scan must still write its
// three parts rather than back off from the older scan's alias rows.
func TestScanFileAudiobookNewerAliasInventoryWinsDB(t *testing.T) {
	f := newConcurrentBookFixture(t)
	alias := f.alias(t)
	for _, name := range []string{"01.mp3", "02.mp3"} {
		if err := f.copyPart(name); err != nil {
			t.Fatal(err)
		}
	}
	newerWalked := make(chan struct{})
	release := make(chan struct{})
	newerDone := make(chan error, 1)
	newer := f.hookedScanner(t, audiobookSkipCheckSQL, func() {
		close(newerWalked)
		<-release
	})
	var hookErr error
	older := f.hookedScanner(t, audiobookSkipCheckSQL, func() {
		if hookErr = f.copyPart("03.mp3"); hookErr != nil {
			return
		}
		go func() {
			newerDone <- newer.ScanFile(context.Background(), filepath.Join(f.bookDir, "03.mp3"), f.folder)
		}()
		select {
		case <-newerWalked:
		case err := <-newerDone:
			hookErr = fmt.Errorf("newer scan finished before its walk was traced: %w", err)
		}
	})

	err := older.ScanFile(t.Context(), filepath.Join(alias, "01.mp3"), f.folder)
	close(release)
	if err != nil {
		t.Fatalf("older ScanFile: %v", err)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err := <-newerDone; err != nil {
		t.Fatalf("newer ScanFile: %v", err)
	}
	f.requireParts(t,
		"Copied Author/01.mp3 "+audiobookMultipartKind+" 1/3",
		"Copied Author/02.mp3 "+audiobookMultipartKind+" 2/3",
		"Copied Author/03.mp3 "+audiobookMultipartKind+" 3/3",
	)
}

// TestScanFileAudiobookKeepsPartRestoredWhileMarkingDB covers a part that is
// gone when a scan walks its book and comes back while that scan is marking it
// missing: another scan indexes it first. The first scan's mark must not hide
// the part that scan just indexed.
func TestScanFileAudiobookKeepsPartRestoredWhileMarkingDB(t *testing.T) {
	f := newConcurrentBookFixture(t)
	for _, name := range []string{"01.mp3", "02.mp3"} {
		if err := f.copyPart(name); err != nil {
			t.Fatal(err)
		}
	}
	other := NewScanner(NewFileRepository(f.pool), f.ffprobe, nil, 2, false, time.Hour)
	if err := other.ScanFile(t.Context(), filepath.Join(f.bookDir, "01.mp3"), f.folder); err != nil {
		t.Fatalf("initial ScanFile: %v", err)
	}
	second := filepath.Join(f.bookDir, "02.mp3")
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	var otherErr error
	ran := false
	s := f.hookedScanner(t, "SET missing_since = $1", func() {
		ran = true
		if otherErr = f.copyPart("02.mp3"); otherErr == nil {
			otherErr = other.ScanFile(context.Background(), second, f.folder)
		}
	})

	if err := s.ScanFile(t.Context(), filepath.Join(f.bookDir, "01.mp3"), f.folder); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !ran {
		t.Fatal("the other scan never ran; the missing mark was not traced")
	}
	if otherErr != nil {
		t.Fatalf("other scan: %v", otherErr)
	}
	f.requireParts(t,
		"Copied Author/01.mp3 "+audiobookMultipartKind+" 1/2",
		"Copied Author/02.mp3 "+audiobookMultipartKind+" 2/2",
	)
}

// TestScanFileAudiobookDiscPartsAgainstConcurrentRowDB covers a book whose
// parts sit in disc folders, so two parts share a file name. A row another
// scan wrote for CD1/01.mp3 after this scan's walk names a part this scan also
// holds, and must not read as one it lacks: the scan still writes the book,
// and CD2 is indexed.
func TestScanFileAudiobookDiscPartsAgainstConcurrentRowDB(t *testing.T) {
	f := newConcurrentBookFixture(t)
	for _, rel := range []string{"CD1/01.mp3", "CD2/01.mp3"} {
		path := filepath.Join(f.bookDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	physical, err := canonicalWalkPath(f.bookDir)
	if err != nil {
		t.Fatal(err)
	}
	contentID := fmt.Sprintf("concurrent-disc-%d", f.folder.ID)
	var hookErr error
	ran := false
	s := f.hookedScanner(t, audiobookSkipCheckSQL, func() {
		ran = true
		bg := context.Background()
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'audiobook', 'Copied Book', 'local', '{}'::text[])`, []any{contentID}},
			{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, []any{contentID, f.folder.ID}},
			{`INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, canonical_root_path, file_size, file_modified_at)
			  VALUES ($1, $2, $3, $4, $5, 1, now())`, []any{contentID, f.folder.ID, filepath.Join(f.bookDir, "CD1", "01.mp3"), f.bookDir, physical}},
		} {
			if _, err := f.pool.Exec(bg, stmt.sql, stmt.args...); err != nil && hookErr == nil {
				hookErr = err
			}
		}
	})

	if err := s.ScanFile(t.Context(), filepath.Join(f.bookDir, "CD2", "01.mp3"), f.folder); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !ran {
		t.Fatal("the concurrent row was never written; the skip-check query was not traced")
	}
	if hookErr != nil {
		t.Fatalf("write concurrent row: %v", hookErr)
	}
	f.requireParts(t,
		"Copied Author/CD1/01.mp3 "+audiobookMultipartKind+" 1/2",
		"Copied Author/CD2/01.mp3 "+audiobookMultipartKind+" 2/2",
	)
}
