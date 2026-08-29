package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

// A chapter thumbnail is extracted from the media file, so losing its bytes is
// not data loss: clearing the selection is what makes internal/chapterthumbs
// re-extract it. Recording a protected loss instead would turn a regenerable
// image into a permanent alert, and — because the reference stays — would keep
// an empty-store rebuild degraded forever.
func TestSignalMissingChapterThumbnailClearsSelectionForRegeneration(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("movie-chapter-loss-%d", suffix)
	root := fmt.Sprintf("/media/chapter-loss-%d", suffix)
	lostPath := fmt.Sprintf("local/movies/%s/deadbeef/still/original.webp", contentID)
	keptPath := fmt.Sprintf("local/movies/%s/cafebabe/still/original.webp", contentID)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Chapter Loss', 'matched', '{}'::text[])
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movie', $1, true) RETURNING id
	`, fmt.Sprintf("chapter-loss-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	chapters := fmt.Sprintf(`[
		{"index": 0, "start_ms": 0, "title": "One", "thumbnail_path": %q,
		 "thumbnail_thumbhash": "lost-hash", "thumbnail_failed_at": "2026-01-01T00:00:00Z",
		 "thumbnail_last_error": "boom"},
		{"index": 1, "start_ms": 60000, "title": "Two", "thumbnail_path": %q,
		 "thumbnail_thumbhash": "kept-hash"}
	]`, lostPath, keptPath)

	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (
			content_id, media_folder_id, file_path, file_size,
			canonical_root_path, observed_root_path, group_key_version,
			content_group_key, base_type, chapters, chapter_thumbnail_retry_after
		)
		VALUES ($1, $2, $3, 1000, $4, $4, 1, $5, 'movie', $6::jsonb, NOW())
		RETURNING id
	`, contentID, folderID, root+"/Film.mkv", root,
		fmt.Sprintf("v1|movie|chapter-loss|%d", suffix), chapters).Scan(&fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (original_path, image_type, not_before)
		VALUES ($1, $2, NOW()), ($3, $2, NOW())
	`, lostPath, ImageCacheImageStill, keptPath); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`,
			[]string{lostPath, keptPath})
	})

	target := artworkurl.Target{
		Surface: artworkurl.SurfaceChapterThumbnails,
		Keys:    []string{fmt.Sprint(fileID), "0"},
		Slot:    ImageCacheImageStill,
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_storage_alerts WHERE surface_name = $1 AND target_keys = $2`,
			target.Surface, target.Keys)
	})

	coordinator := NewArtworkDeliveryCoordinator(pool, nil)
	state, err := coordinator.LoadTarget(ctx, target)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if state.SelectedPath != lostPath {
		t.Fatalf("SelectedPath = %q, want %q", state.SelectedPath, lostPath)
	}
	if err := coordinator.SignalMissing(ctx, state); err != nil {
		t.Fatalf("SignalMissing: %v", err)
	}

	var raw []byte
	var retryAfter *time.Time
	if err := pool.QueryRow(ctx, `SELECT chapters, chapter_thumbnail_retry_after FROM media_files WHERE id = $1`,
		fileID).Scan(&raw, &retryAfter); err != nil {
		t.Fatalf("read chapters: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode chapters: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("chapters = %d, want 2 (order and siblings must survive)", len(decoded))
	}
	if decoded[0]["thumbnail_path"] != "" || decoded[0]["thumbnail_thumbhash"] != "" {
		t.Fatalf("cleared chapter = %+v, want an empty thumbnail selection", decoded[0])
	}
	if _, ok := decoded[0]["thumbnail_last_error"]; ok {
		t.Fatalf("cleared chapter kept a failure marker: %+v", decoded[0])
	}
	if decoded[0]["title"] != "One" || decoded[1]["title"] != "Two" {
		t.Fatalf("chapter order = %+v, want One then Two", decoded)
	}
	if decoded[1]["thumbnail_path"] != keptPath {
		t.Fatalf("sibling chapter = %+v, want its thumbnail untouched", decoded[1])
	}
	if retryAfter != nil {
		t.Fatalf("chapter_thumbnail_retry_after = %v, want NULL so re-extraction is due", retryAfter)
	}

	var repairState string
	var missingAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT repair_state, missing_at FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, lostPath).Scan(&repairState, &missingAt); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if repairState != ArtworkRepairStatePending || missingAt == nil {
		t.Fatalf("repair_state = %q missing_at = %v, want a queued regeneration", repairState, missingAt)
	}

	var alerts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_storage_alerts
		WHERE kind = 'protected_data_loss' AND surface_name = $1 AND target_keys = $2 AND resolved_at IS NULL`,
		target.Surface, target.Keys).Scan(&alerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts != 0 {
		t.Fatalf("protected data-loss alerts = %d, want 0 for a regenerable chapter thumbnail", alerts)
	}
}

// ClaimDueRepairs is what keeps an empty-store rebuild alive under
// artwork.remote_materialization=passthrough, so it must claim repair jobs and
// leave ordinary materialization work queued.
func TestClaimDueRepairsClaimsOnlyRepairJobs(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	repairID := fmt.Sprintf("movie-repair-%d", suffix)
	ordinaryID := fmt.Sprintf("movie-ordinary-%d", suffix)

	repo := NewImageCacheJobRepository(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = ANY($1)`,
			[]string{repairID, ordinaryID})
	})
	if _, err := repo.EnqueueRepair(ctx, EnqueueImageCacheJobInput{
		TargetType: ImageCacheTargetItem, TargetContentID: repairID,
		SourcePath: "tmdb://poster/repair.jpg", ImageType: ImageCacheImagePoster,
	}); err != nil {
		t.Fatalf("enqueue repair: %v", err)
	}
	if err := repo.Enqueue(ctx, EnqueueImageCacheJobInput{
		TargetType: ImageCacheTargetItem, TargetContentID: ordinaryID,
		SourcePath: "tmdb://poster/ordinary.jpg", ImageType: ImageCacheImagePoster,
	}); err != nil {
		t.Fatalf("enqueue ordinary: %v", err)
	}

	claimed, err := repo.ClaimDueRepairs(ctx, fmt.Sprintf("repair-worker-%d", suffix), 10)
	if err != nil {
		t.Fatalf("ClaimDueRepairs: %v", err)
	}
	for _, job := range claimed {
		if job.TargetContentID == ordinaryID {
			t.Fatalf("claimed an ordinary materialization job %d in repair-only mode", job.ID)
		}
	}
	found := false
	for _, job := range claimed {
		if job.TargetContentID == repairID {
			found = true
		}
	}
	if !found {
		t.Fatalf("repair job for %s was not claimed", repairID)
	}

	var ordinaryStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM metadata_image_cache_jobs WHERE target_content_id = $1`,
		ordinaryID).Scan(&ordinaryStatus); err != nil {
		t.Fatalf("read ordinary job: %v", err)
	}
	if ordinaryStatus != "queued" {
		t.Fatalf("ordinary job status = %q, want it left queued while materialization is off", ordinaryStatus)
	}
}
