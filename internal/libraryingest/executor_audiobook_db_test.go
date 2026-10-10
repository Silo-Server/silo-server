package libraryingest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type audiobookPartRow struct {
	kind           string
	index          int
	total          int
	probeUpdatedAt time.Time
}

// TestIngestKeepsAudiobookPartsDB runs library ingest over multipart
// audiobooks whose file names the video variant parser reads differently from
// the audiobook scanner: plain track numbers, an intro before named parts, and
// disc-numbered tracks. Each book must keep the scanner's part order, and a
// second ingest of unchanged files must skip every book.
func TestIngestKeepsAudiobookPartsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	sample := filepath.Join(t.TempDir(), "sample.mp3")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-map_metadata", "-1", sample).CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v: %s", err, out)
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	// Parts in the natural path order the audiobook scanner plays them.
	books := map[string][]string{
		"Numbered Tracks": {"01.mp3", "02.mp3", "03.mp3"},
		"Intro And Parts": {"Intro.mp3", "Part 1.mp3", "Part 2.mp3"},
		"Two Discs":       {"Disc 1 - Track 2.mp3", "Disc 1 - Track 10.mp3", "Disc 2 - Track 1.mp3"},
	}
	for book, parts := range books {
		dir := filepath.Join(root, book)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			if err := os.WriteFile(filepath.Join(dir, part), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', 'Audiobook parts ingest', true) RETURNING id`,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id = $1)`, folderID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, root); err != nil {
		t.Fatal(err)
	}
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}, Enabled: true}
	fileRepo := scanner.NewFileRepository(pool)
	scan := scanner.NewScanner(fileRepo, ffprobe, nil, 2, true, 0)
	ingester := &Executor{scanner: scan, matcher: &retryRecordingMatcher{}, now: time.Now}

	ingest := func() {
		t.Helper()
		if _, err := ingester.IngestFolder(ctx, folder); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	rows := func() map[string]audiobookPartRow {
		t.Helper()
		result, err := pool.Query(ctx, `
			SELECT file_path, presentation_kind, COALESCE(presentation_part_index, 0),
			       COALESCE(presentation_part_total, 0), probe_updated_at
			FROM media_files WHERE media_folder_id = $1 AND missing_since IS NULL`, folderID)
		if err != nil {
			t.Fatal(err)
		}
		defer result.Close()
		out := map[string]audiobookPartRow{}
		for result.Next() {
			var path string
			var row audiobookPartRow
			if err := result.Scan(&path, &row.kind, &row.index, &row.total, &row.probeUpdatedAt); err != nil {
				t.Fatal(err)
			}
			out[path] = row
		}
		if err := result.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	checkParts := func(stage string) map[string]audiobookPartRow {
		t.Helper()
		got := rows()
		for book, parts := range books {
			var contentID string
			for i, part := range parts {
				path := filepath.Join(root, book, part)
				row, ok := got[path]
				if !ok {
					t.Fatalf("%s: %s/%s has no live row", stage, book, part)
				}
				if row.kind != "multipart" || row.index != i+1 || row.total != len(parts) {
					t.Errorf("%s: %s/%s is %q part %d of %d, want multipart part %d of %d",
						stage, book, part, row.kind, row.index, row.total, i+1, len(parts))
				}
				if contentID == "" {
					if err := pool.QueryRow(ctx, `SELECT content_id FROM media_files WHERE file_path = $1 AND missing_since IS NULL`, path).Scan(&contentID); err != nil {
						t.Fatal(err)
					}
				}
			}
			files, err := fileRepo.GetByContentIDPresentation(ctx, contentID)
			if err != nil {
				t.Fatal(err)
			}
			order := make([]string, 0, len(files))
			for _, file := range files {
				order = append(order, filepath.Base(file.FilePath))
			}
			if !reflect.DeepEqual(order, parts) {
				t.Errorf("%s: %s plays %v, want %v", stage, book, order, parts)
			}
		}
		return got
	}
	checkSkipped := func(stage string, before, after map[string]audiobookPartRow) {
		t.Helper()
		for path, row := range before {
			if !after[path].probeUpdatedAt.Equal(row.probeUpdatedAt) {
				t.Errorf("%s: unchanged %s was probed again", stage, path)
			}
		}
	}

	ingest()
	first := checkParts("first ingest")
	ingest()
	checkSkipped("second ingest", first, checkParts("second ingest"))

	// Rows that an earlier build's variant pass already rewrote are rebuilt
	// once by the next ingest and then skipped again.
	if err := scan.FinalizeVariantsByPathPrefix(ctx, folder, root); err != nil {
		t.Fatal(err)
	}
	ingest()
	healed := checkParts("ingest after rewritten rows")
	ingest()
	checkSkipped("ingest after repair", healed, checkParts("ingest after repair"))
}
