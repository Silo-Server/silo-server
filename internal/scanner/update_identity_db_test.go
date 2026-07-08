package scanner

import (
	"context"
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
// content linkage untouched — no ffprobe, no probe-column churn.
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

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', 'UI Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, media_folder_id, file_path, file_size,
			observed_root_path, canonical_root_path, content_group_key, group_key_version,
			base_title, base_year, base_type,
			codec_video, codec_audio, resolution, container, duration, bitrate,
			video_tracks, audio_tracks, chapters, probe_source, probe_updated_at
		) VALUES (
			$1, $2, $3, 123456,
			'/old/root', '/old/root', 'v1|movie|movie|2020', 1,
			'Movie', 2020, 'movie',
			'h264', 'aac', '1080p', 'mkv', 7200, 5000,
			'[{"index":0}]'::jsonb, '[{"index":1}]'::jsonb, '[]'::jsonb, 'local', $4
		) RETURNING id
	`, contentID, folderID, path, probedAt).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}

	repo := NewFileRepository(pool)
	updated, err := repo.UpdateIdentity(ctx, models.MediaFile{
		MediaFolderID:     folderID,
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

	// Identity/grouping columns rewritten.
	if updated.ContentGroupKey != "v1|movie|anchor|tvdb-1" {
		t.Errorf("content_group_key = %q, want anchored form", updated.ContentGroupKey)
	}
	if updated.ObservedRootPath != "/new/root" {
		t.Errorf("observed_root_path = %q, want /new/root", updated.ObservedRootPath)
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
}
