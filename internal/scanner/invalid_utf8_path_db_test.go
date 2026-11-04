package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

// strictFakeFFprobe answers like ffprobe only when its last argument names a
// file that exists, so a probe handed a lossy or encoded path fails.
const strictFakeFFprobe = `#!/bin/sh
for last; do :; done
[ -e "$last" ] || { echo "$last: No such file or directory" >&2; exit 1; }
printf '%s\n' '{"format":{"duration":"1500.000000","size":"5","bit_rate":"1000"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"25/1"}]}'
`

// A Latin-1 file name (the issue's "B\xe5rd Ylvis\xe5ker") is not valid
// UTF-8. The scan stores it in encoded form, probes the real file, parses the
// show title, keeps the sidecar subtitle's path openable, and finds the row
// again on the next scan instead of inserting it twice.
func TestScanIndexesFileWithNonUTF8Name(t *testing.T) {
	ctx := t.Context()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	seasonDir := filepath.Join(root, "Takk for alt (2023) {imdb-}", "Season 01")
	stem := "Takk for alt (2023) - S01E01 - Takk for alt B\xe5rd Ylvis\xe5ker [WEBDL-1080p][AAC 2.0][x264]-UNGERDAS"
	rawVideo := filepath.Join(seasonDir, stem+".mkv")
	rawSubtitle := filepath.Join(seasonDir, stem+".no.srt")
	writeTestFile(t, rawVideo, "video")
	writeTestFile(t, rawSubtitle, "1\n00:00:01,000 --> 00:00:02,000\nHei\n")

	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobePath, strictFakeFFprobe)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', 'Non-UTF-8 Path Test', true)
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, root); err != nil {
		t.Fatalf("seed folder path: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1
			)`, folderID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	folder := &models.MediaFolder{ID: folderID, Type: "series", Paths: []string{root}, Enabled: true}

	scanner := NewScanner(NewFileRepository(pool), ffprobePath, nil, 1, false, 0)
	for pass := 1; pass <= 2; pass++ {
		result, err := scanner.ScanFolder(ctx, folder)
		if err != nil {
			t.Fatalf("scan %d: %v", pass, err)
		}
		if result.Errors != 0 {
			t.Fatalf("scan %d reported %d file errors", pass, result.Errors)
		}
	}

	files := readNonUTF8TestFiles(ctx, t, pool, folderID)
	if len(files) != 1 {
		t.Fatalf("got %d media_files rows after two scans, want 1: %+v", len(files), files)
	}
	got := files[0]
	if want := fspath.Encode(rawVideo); got.path != want {
		t.Fatalf("file_path = %q, want encoded %q", got.path, want)
	}
	if fspath.Decode(got.path) != rawVideo {
		t.Fatalf("stored path %q does not decode to the file on disk", got.path)
	}
	if got.codec != "h264" {
		t.Fatalf("codec_video = %q, want h264 from probing the real file", got.codec)
	}
	if got.baseTitle != "Takk for alt" {
		t.Fatalf("base_title = %q, want %q", got.baseTitle, "Takk for alt")
	}

	var subtitles []models.ExternalSubtitle
	if err := json.Unmarshal(got.subtitles, &subtitles); err != nil {
		t.Fatalf("decode external_subtitles %s: %v", got.subtitles, err)
	}
	if len(subtitles) != 1 {
		t.Fatalf("external_subtitles = %s, want the one sidecar", got.subtitles)
	}
	if fspath.Decode(subtitles[0].Path) != rawSubtitle {
		t.Fatalf("subtitle path %q does not decode to the sidecar on disk", subtitles[0].Path)
	}
	if _, err := fspath.Stat(subtitles[0].Path); err != nil {
		t.Fatalf("stat stored subtitle path: %v", err)
	}
	if want := fspath.Display(filepath.Base(subtitles[0].Path)); subtitles[0].Title != want {
		t.Fatalf("subtitle title = %q, want the display name %q", subtitles[0].Title, want)
	}
}

type nonUTF8TestFile struct {
	path, baseTitle, codec string
	subtitles              []byte
}

func readNonUTF8TestFiles(ctx context.Context, t *testing.T, pool *pgxpool.Pool, folderID int) []nonUTF8TestFile {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT file_path, COALESCE(base_title, ''), COALESCE(codec_video, ''), external_subtitles
		FROM media_files WHERE media_folder_id = $1
	`, folderID)
	if err != nil {
		t.Fatalf("read media files: %v", err)
	}
	defer rows.Close()
	var files []nonUTF8TestFile
	for rows.Next() {
		var row nonUTF8TestFile
		if err := rows.Scan(&row.path, &row.baseTitle, &row.codec, &row.subtitles); err != nil {
			t.Fatalf("scan media file: %v", err)
		}
		files = append(files, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read media files: %v", err)
	}
	return files
}
