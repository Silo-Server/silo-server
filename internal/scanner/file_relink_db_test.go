package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// writeRelinkTestMedia writes a file large enough for an OSHash (128 KiB or
// more); seed makes its bytes differ from another file of the same size.
func writeRelinkTestMedia(t *testing.T, path string, seed byte) {
	t.Helper()
	data := make([]byte, 256<<10)
	for i := range data {
		data[i] = byte(i) ^ seed
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestScanKeepsFileRowAcrossRenameDB checks that a renamed or moved file keeps
// its media_files row, and with it the state keyed to the row's id, instead of
// leaving that state on a missing row while the new path starts over.
func TestScanKeepsFileRowAcrossRenameDB(t *testing.T) {
	pool := newScannerTestPool(t)
	cases := []relinkCase{
		{
			name:        "rename within a folder",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020)/Movie (2020) - 1080p.mkv",
			wantSameRow: true,
		},
		{
			name:        "move across folders",
			from:        "Shelf A/Movie (2020)/Movie (2020).mkv",
			to:          "Shelf B/Movie (2020)/Movie (2020).mkv",
			wantSameRow: true,
		},
		{
			name:         "move after the old path was marked missing",
			from:         "Movie (2020)/Movie (2020).mkv",
			to:           "Movies/Movie (2020)/Movie (2020).mkv",
			missingFirst: true,
			wantSameRow:  true,
		},
		{
			name:        "rename reported by file, new path first",
			from:        "Movie (2020)/Movie.2020.1080p.mkv",
			to:          "Movie (2020)/Movie (2020).mkv",
			byFile:      true,
			wantSameRow: true,
		},
		{
			name:       "daily episode date correction starts over",
			folderType: "series",
			from:       "Daily Show/Daily Show - 2026-04-24.mkv",
			to:         "Daily Show/Daily Show - 2026-04-25.mkv",
		},
		{
			name:       "episode range change starts over",
			folderType: "series",
			from:       "Show/Season 1/Show S01E01.mkv",
			to:         "Show/Season 1/Show S01E01-E02.mkv",
		},
		{
			name: "copy keeps the original row in place",
			from: "Movie (2020)/Movie (2020).mkv",
			to:   "Backup/Movie (2020)/Movie (2020).mkv",
			copy: true,
		},
		{
			name: "rename to another title starts over",
			from: "Movie (2020)/Movie (2020).mkv",
			to:   "Other Film (2019)/Other Film (2019).mkv",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkRelinkCase(t, pool, tc) })
	}
}

type relinkCase struct {
	name       string
	folderType string
	from       string
	to         string
	// copy leaves the original in place, so the new path is a second file.
	copy bool
	// missingFirst scans once with the file gone from both paths, so the
	// original row is already marked missing when the new path shows up.
	missingFirst bool
	// byFile delivers the rename as an arr webhook does: a scan of the new
	// path, then of the old one.
	byFile bool
	// wantSameRow is whether the file at to keeps the original row.
	wantSameRow bool
}

func checkRelinkCase(t *testing.T, pool *pgxpool.Pool, tc relinkCase) {
	ctx := t.Context()
	root := t.TempDir()
	folderType := tc.folderType
	if folderType == "" {
		folderType = "movies"
	}
	folderID := seedDeadRootTestFolder(t, pool, folderType, fmt.Sprintf("relink-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: folderType, Paths: []string{root}}
	repo := NewFileRepository(pool)
	s := NewScanner(repo, "", nil, 2, false, time.Hour)

	from := filepath.Join(root, tc.from)
	to := filepath.Join(root, tc.to)
	writeRelinkTestMedia(t, from, 1)
	// Another title keeps the library from walking empty, which would hold
	// back missing-marking.
	writeRelinkTestMedia(t, filepath.Join(root, "Keeper (2010)", "Keeper (2010).mkv"), 9)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	original, err := repo.GetByPath(ctx, from)
	if err != nil {
		t.Fatalf("GetByPath(original): %v", err)
	}
	// A user's subtitle timing correction stands in for the state keyed to
	// the row's id.
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_subtitle_timings (media_file_id, content_sha256, path, format, timing_offset_ms)
		VALUES ($1, $2, $3, 'srt', 1500)`,
		original.ID, strings.Repeat("a", 64), strings.TrimSuffix(from, ".mkv")+".srt",
	); err != nil {
		t.Fatalf("seed subtitle timing: %v", err)
	}

	relocateRelinkFile(t, s, folder, from, to, tc)

	moved, err := repo.GetByPath(ctx, to)
	if err != nil {
		t.Fatalf("GetByPath(new path): %v", err)
	}
	if moved.MissingSince != nil {
		t.Fatalf("file at the new path is marked missing")
	}
	if got := moved.ID == original.ID; got != tc.wantSameRow {
		t.Fatalf("new path kept the original row = %v, want %v (original %d, now %d)", got, tc.wantSameRow, original.ID, moved.ID)
	}
	var timings int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM external_subtitle_timings WHERE media_file_id = $1`, moved.ID,
	).Scan(&timings); err != nil {
		t.Fatal(err)
	}
	wantTimings := 0
	if tc.wantSameRow {
		wantTimings = 1
	}
	if timings != wantTimings {
		t.Fatalf("subtitle timings on the new path's row = %d, want %d", timings, wantTimings)
	}
	if !tc.wantSameRow {
		return
	}

	// A different file later placed at the old path is a new file, not the
	// one that moved away.
	writeRelinkTestMedia(t, from, 2)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan with a new file at the old path: %v", err)
	}
	replacement, err := repo.GetByPath(ctx, from)
	if err != nil {
		t.Fatalf("GetByPath(old path): %v", err)
	}
	if replacement.ID == original.ID {
		t.Fatalf("new file at the old path took the moved file's row %d", original.ID)
	}
	stillMoved, err := repo.GetByPath(ctx, to)
	if err != nil {
		t.Fatalf("GetByPath(new path) after replacement: %v", err)
	}
	if stillMoved.ID != original.ID || stillMoved.MissingSince != nil {
		t.Fatalf("moved file row = %d missing=%v, want %d present", stillMoved.ID, stillMoved.MissingSince, original.ID)
	}
}

// relocateRelinkFile moves (or copies) the scanned file from from to to and
// scans the change the way the case describes.
func relocateRelinkFile(t *testing.T, s *Scanner, folder *models.MediaFolder, from, to string, tc relinkCase) {
	t.Helper()
	ctx := t.Context()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	source := from
	if tc.missingFirst {
		source = filepath.Join(t.TempDir(), "parked.mkv")
		if err := os.Rename(from, source); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("scan with the file gone: %v", err)
		}
		if gone, err := s.fileRepo.GetByPath(ctx, from); err != nil || gone.MissingSince == nil {
			t.Fatalf("original row is not marked missing after the file left (err %v)", err)
		}
	}
	if tc.copy {
		writeRelinkTestMedia(t, to, 1)
	} else if err := os.Rename(source, to); err != nil {
		t.Fatal(err)
	}
	if !tc.byFile {
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("scan after rename: %v", err)
		}
		return
	}
	if err := s.ScanFile(ctx, to, folder); err != nil {
		t.Fatalf("scan of the new path: %v", err)
	}
	if err := s.ScanFile(ctx, from, folder); err != nil {
		t.Fatalf("scan of the old path: %v", err)
	}
}
