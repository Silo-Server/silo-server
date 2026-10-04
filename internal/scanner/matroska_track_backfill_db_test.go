package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func insertMatroskaBackfillRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string, size int64, mtime *time.Time, subtitles string) int {
	t.Helper()
	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("MKV Track Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("insert media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, file_modified_at,
			container, video_tracks, audio_tracks, subtitle_tracks)
		VALUES ($1, $2, $3, $4, $5, 'mkv', '[{"codec":"h264"}]', '[{"codec":"opus"}]', $6::jsonb)
		RETURNING id`,
		fmt.Sprintf("mkv-track-content-%d", suffix), folderID, path, size, mtime, subtitles,
	).Scan(&fileID); err != nil {
		t.Fatalf("insert media file: %v", err)
	}
	return fileID
}

func readBackfilledSubtitleTracks(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID int) []models.SubtitleTrack {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT subtitle_tracks FROM media_files WHERE id = $1`, fileID).Scan(&raw); err != nil {
		t.Fatalf("read subtitle_tracks: %v", err)
	}
	var tracks []models.SubtitleTrack
	if err := json.Unmarshal(raw, &tracks); err != nil {
		t.Fatalf("decode subtitle_tracks: %v", err)
	}
	return tracks
}

// The backfill writes IDs only for a file that still matches its stored
// probe, sets nothing but container_track_id, and leaves every other row as it
// found it. A row whose tracks do not decode must not end the pass.
func TestMatroskaTrackBackfillRecordsTrackNumbers(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	fixture, err := os.ReadFile("testdata/subtitles.mkv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyFixture := func(name string) (string, os.FileInfo) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, fixture, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return path, info
	}

	// Lowest ID, so with one row per batch it is a batch of its own.
	path, info := copyFixture("Undecodable (2020).mkv")
	insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), `[{"index":"two","codec":"subrip"}]`)

	const subtitles = `[{"index":2,"codec":"subrip","language":"eng","title":"SRT","forced":false,"default":false,"hearing_impaired":false,"external":false,"future_field":"kept"},
		{"index":3,"codec":"ass","language":"jpn","forced":false,"default":false,"hearing_impaired":false,"external":false}]`
	path, info = copyFixture("Matched (2020).mkv")
	matched := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), subtitles)
	// A row stored without an mtime is checked on size alone.
	path, info = copyFixture("No Mtime (2020).mkv")
	noMtime := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), nil, subtitles)
	// The row describes a different revision of the file on disk.
	path, info = copyFixture("Changed (2020).mkv")
	changed := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size()+1, new(info.ModTime()), subtitles)
	// The stored layout puts a subtitle where the file has its audio track.
	path, info = copyFixture("Unmatched (2020).mkv")
	unmatched := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()),
		`[{"index":1,"codec":"subrip","language":"eng"},{"index":3,"codec":"ass","language":"jpn"}]`)

	backfiller := NewMatroskaTrackBackfiller(NewFileRepository(pool))
	backfiller.batch = 1
	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int{matched, noMtime} {
		tracks := readBackfilledSubtitleTracks(t, ctx, pool, id)
		if len(tracks) != 2 || tracks[0].ContainerTrackID != "3" || tracks[1].ContainerTrackID != "4" {
			t.Fatalf("file %d tracks = %+v, want container IDs 3 and 4", id, tracks)
		}
		if tracks[0].Title != "SRT" || tracks[0].Language != "eng" || tracks[1].Codec != "ass" {
			t.Fatalf("backfill changed other track fields: %+v", tracks)
		}
	}
	var kept string
	if err := pool.QueryRow(ctx, `SELECT subtitle_tracks->0->>'future_field' FROM media_files WHERE id = $1`, matched).Scan(&kept); err != nil || kept != "kept" {
		t.Fatalf("unknown subtitle field = %q (%v), want it preserved", kept, err)
	}
	for _, id := range []int{changed, unmatched} {
		for _, track := range readBackfilledSubtitleTracks(t, ctx, pool, id) {
			if track.ContainerTrackID != "" {
				t.Fatalf("file %d got container ID %q, want none", id, track.ContainerTrackID)
			}
		}
	}

	// A file with its IDs is no longer a candidate.
	candidates, _, _, err := backfiller.loadCandidates(ctx, matched-1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.id == matched {
			t.Fatalf("file %d is still a candidate after its backfill", matched)
		}
	}
}
