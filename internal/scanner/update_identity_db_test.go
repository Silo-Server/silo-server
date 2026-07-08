package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestUpdateIdentityPreservesProbeData covers the scanner's metadata-only update
// path (issue #319 hardening): rewriting a file's derived identity/grouping must
// persist the new root/group columns while leaving probe data, file bytes, and
// content linkage untouched — no ffprobe, no probe-column churn. Like every
// scan write it must clear match suppression, and it must follow folder moves.
func TestUpdateIdentityPreservesProbeData(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("ui-content-%d", suffix)
	path := fmt.Sprintf("/tmp/ui-%d/Movie (2020) {tvdb-1}/Movie (2020).mkv", suffix)
	probedAt := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)

	var folderID, movedFolderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', 'UI Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', 'UI Test Moved', true) RETURNING id
	`).Scan(&movedFolderID); err != nil {
		t.Fatalf("seed moved folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = ANY($1)`, []int{folderID, movedFolderID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{folderID, movedFolderID})
	})

	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, media_folder_id, file_path, file_size,
			observed_root_path, canonical_root_path, content_group_key, group_key_version,
			base_title, base_year, base_type,
			codec_video, codec_audio, resolution, container, duration, bitrate,
			video_tracks, audio_tracks, chapters, probe_source, probe_updated_at,
			match_suppressed_at
		) VALUES (
			$1, $2, $3, 123456,
			'/old/root', '/old/root', 'v1|movie|movie|2020', 1,
			'Movie', 2020, 'movie',
			'h264', 'aac', '1080p', 'mkv', 7200, 5000,
			'[{"index":0}]'::jsonb, '[{"index":1}]'::jsonb, '[]'::jsonb, 'local', $4,
			NOW()
		) RETURNING id
	`, contentID, folderID, path, probedAt).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	repo := NewFileRepository(pool)
	updatedID, err := repo.UpdateIdentity(ctx, models.MediaFile{
		MediaFolderID:     movedFolderID,
		FilePath:          path,
		ObservedRootPath:  "/new/root",
		CanonicalRootPath: "/new/root",
		ContentGroupKey:   "v1|movie|anchor|tvdb-1",
		GroupKeyVersion:   1,
		BaseTitle:         "Movie",
		BaseYear:          2020,
		BaseType:          "movie",
	})
	if err != nil {
		t.Fatalf("UpdateIdentity: %v", err)
	}
	if updatedID != fileID {
		t.Errorf("UpdateIdentity id = %d, want %d", updatedID, fileID)
	}

	updated, err := repo.GetByPath(ctx, path)
	if err != nil {
		t.Fatalf("GetByPath after UpdateIdentity: %v", err)
	}

	// Identity/grouping columns rewritten.
	if updated.ContentGroupKey != "v1|movie|anchor|tvdb-1" {
		t.Errorf("content_group_key = %q, want anchored form", updated.ContentGroupKey)
	}
	if updated.ObservedRootPath != "/new/root" {
		t.Errorf("observed_root_path = %q, want /new/root", updated.ObservedRootPath)
	}
	if updated.CanonicalRootPath != "/new/root" {
		t.Errorf("canonical_root_path = %q, want /new/root", updated.CanonicalRootPath)
	}
	if updated.BaseTitle != "Movie" || updated.BaseYear != 2020 || updated.BaseType != "movie" {
		t.Errorf("base title/year/type = %q/%d/%q, want Movie/2020/movie",
			updated.BaseTitle, updated.BaseYear, updated.BaseType)
	}
	if updated.MediaFolderID != movedFolderID {
		t.Errorf("media_folder_id = %d, want moved folder %d", updated.MediaFolderID, movedFolderID)
	}

	// Probe data and linkage preserved.
	if updated.ContentID != contentID {
		t.Errorf("content_id = %q, want preserved %q", updated.ContentID, contentID)
	}
	if updated.CodecVideo != "h264" || updated.CodecAudio != "aac" || updated.Resolution != "1080p" {
		t.Errorf("probe codecs mutated: video=%q audio=%q res=%q", updated.CodecVideo, updated.CodecAudio, updated.Resolution)
	}
	if updated.Duration != 7200 {
		t.Errorf("duration = %d, want preserved 7200", updated.Duration)
	}
	if updated.ProbeSource != "local" {
		t.Errorf("probe_source = %q, want preserved local", updated.ProbeSource)
	}
	if updated.ProbeUpdatedAt == nil || !updated.ProbeUpdatedAt.Equal(probedAt) {
		t.Errorf("probe_updated_at = %v, want preserved %v", updated.ProbeUpdatedAt, probedAt)
	}
	if len(updated.VideoTracks) != 1 || len(updated.AudioTracks) != 1 {
		t.Errorf("track arrays mutated: video=%d audio=%d", len(updated.VideoTracks), len(updated.AudioTracks))
	}
	if updated.FileSize != 123456 {
		t.Errorf("file_size = %d, want preserved 123456", updated.FileSize)
	}

	// Match suppression cleared like any other scan write, so the fresh
	// identity re-enters the match backlog.
	var suppressed bool
	if err := pool.QueryRow(ctx, `
		SELECT match_suppressed_at IS NOT NULL FROM media_files WHERE id = $1
	`, fileID).Scan(&suppressed); err != nil {
		t.Fatalf("read match_suppressed_at: %v", err)
	}
	if suppressed {
		t.Error("match_suppressed_at still set, want cleared by identity update")
	}

	// A vanished row surfaces as ErrFileNotFound so the scanner can fall back
	// to the full upsert path.
	if _, err := repo.UpdateIdentity(ctx, models.MediaFile{
		MediaFolderID: folderID,
		FilePath:      path + ".does-not-exist",
	}); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("UpdateIdentity on missing row: err = %v, want ErrFileNotFound", err)
	}
}
