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

// backfillCandidateIDs returns every file the next pass would read.
func backfillCandidateIDs(t *testing.T, ctx context.Context, b *MatroskaTrackBackfiller) map[int]bool {
	t.Helper()
	ids := map[int]bool{}
	for afterID := 0; ; {
		candidates, lastID, rowCount, err := b.loadCandidates(ctx, afterID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range candidates {
			ids[c.id] = true
		}
		if rowCount < b.batch {
			return ids
		}
		afterID = lastID
	}
}

// A pass reads only files with a SubRip or ASS track lacking an ID, and
// records each file it reads, so the next pass reads none of them again until
// the row changes. Storage that cannot open a file, and a file that differs
// from its row until a scan rewrites it, are not recorded.
func TestMatroskaTrackBackfillReadsEachFileOnce(t *testing.T) {
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
	writeFile := func(name string, data []byte) (string, os.FileInfo) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return path, info
	}
	const subtitles = `[{"index":2,"codec":"subrip","language":"eng"},{"index":3,"codec":"ass","language":"jpn"}]`

	path, info := writeFile("Matched (2020).mkv", fixture)
	matched := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), subtitles)
	const misplaced = `[{"index":1,"codec":"subrip","language":"eng"},{"index":3,"codec":"ass","language":"jpn"}]`
	path, info = writeFile("Unmatched (2020).mkv", fixture)
	unmatched := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), misplaced)
	path, info = writeFile("Unmatched Again (2020).mkv", fixture)
	unmatchedAgain := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), misplaced)
	path, info = writeFile("Changed (2020).mkv", fixture)
	changed := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size()+1, new(info.ModTime()), subtitles)
	path, info = writeFile("Not Matroska (2020).mkv", []byte("RIFF\x00\x00\x00\x00AVI LIST"))
	notMatroska := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), subtitles)
	missing := insertMatroskaBackfillRow(t, ctx, pool, filepath.Join(dir, "Missing (2020).mkv"), 1, nil, subtitles)
	// Without a stored size the file's revision cannot be checked.
	path, info = writeFile("No Size (2020).mkv", fixture)
	noSize := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()), subtitles)
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_size = NULL WHERE id = $1`, noSize); err != nil {
		t.Fatal(err)
	}
	// No client plays a PGS track by its Matroska track number, so the file
	// is never read.
	path, info = writeFile("PGS Only (2020).mkv", fixture)
	pgsOnly := insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()),
		`[{"index":2,"codec":"hdmv_pgs_subtitle","language":"eng"}]`)

	backfiller := NewMatroskaTrackBackfiller(NewFileRepository(pool))
	before := backfillCandidateIDs(t, ctx, backfiller)
	for _, id := range []int{matched, unmatched, unmatchedAgain, changed, notMatroska, missing} {
		if !before[id] {
			t.Fatalf("file %d is not a candidate before its first pass", id)
		}
	}
	if before[pgsOnly] || before[noSize] {
		t.Fatalf("PGS-only file %d or sizeless file %d is a candidate", pgsOnly, noSize)
	}

	if _, err := backfiller.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	after := backfillCandidateIDs(t, ctx, backfiller)
	for _, id := range []int{matched, unmatched, unmatchedAgain, notMatroska} {
		if after[id] {
			t.Fatalf("file %d is still a candidate after its pass", id)
		}
	}
	if !after[missing] || !after[changed] {
		t.Fatalf("unopenable file %d or changed file %d is no longer a candidate", missing, changed)
	}
	// The updated row's check matches the tracks as written.
	var current bool
	if err := pool.QueryRow(ctx, `
		SELECT c.probe_md5 = md5(jsonb_build_array(mf.video_tracks, mf.audio_tracks, mf.subtitle_tracks)::text)
		FROM matroska_track_backfill_checks c JOIN media_files mf ON mf.id = c.media_file_id
		WHERE mf.id = $1`, matched).Scan(&current); err != nil || !current {
		t.Fatalf("check of updated file %d is current = %v (%v), want true", matched, current, err)
	}

	// A reprobe that rewrites the file revision or the tracks the match reads
	// brings a file back, and so does a check made under older matching rules.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_modified_at = now() WHERE id = $1`, unmatched); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET video_tracks = '[]' WHERE id = $1`, notMatroska); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE matroska_track_backfill_checks SET matcher_version = $2 WHERE media_file_id = $1`,
		unmatchedAgain, matroskaTrackMatcherVersion-1); err != nil {
		t.Fatal(err)
	}
	again := backfillCandidateIDs(t, ctx, backfiller)
	for _, id := range []int{unmatched, notMatroska, unmatchedAgain} {
		if !again[id] {
			t.Fatalf("file %d is not a candidate after its row or the matcher changed", id)
		}
	}
	if again[matched] {
		t.Fatalf("file %d with every ID is a candidate again", matched)
	}
}
