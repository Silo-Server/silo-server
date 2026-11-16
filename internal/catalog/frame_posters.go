package catalog

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
)

// PendingFramePoster is a movie with no poster that the extras frame task
// gives one taken from its own video. Its poster_source_path is empty, or the
// StillSourceFramePrefix marker of a frame whose stored copy was lost.
type PendingFramePoster struct {
	ContentID    string
	PosterSource string // the source to compare against when saving
	FileID       int
	Duration     int    // seconds, 0 when unknown
	FrameKey     string // identifies the file's current content
}

// ListPendingFramePosters returns up to limit movies with no poster and no
// image on its way, one live main file each: the lowest id. A sidecar or
// provider poster waiting for its copy keeps a source of its own, so the
// movie is not listed; a frame whose copy the artwork reconciler cleared
// keeps its frame source, so it is listed again. A movie waits while it is
// pending its first match, while its images are locked, while its file is in
// a disabled library or not probed yet (the frame position depends on the
// duration), and while the file's media analysis artifact under key says it
// cannot be decoded, or that its last attempt on node failed and is backing
// off.
//
// The empty poster_path is an equality probe on idx_media_items_poster_path_gc,
// so the scan reads only the items with no poster, and each of them joins its
// files through the content_id index and its artifact through the primary key.
func (r *ItemRepository) ListPendingFramePosters(ctx context.Context, failures mediaartifact.Key, node string, limit int) ([]PendingFramePoster, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.content_id, c.poster_source_path, c.file_id, c.duration, c.frame_key
		FROM (
			SELECT DISTINCT ON (mi.content_id)
				mi.content_id, mi.poster_source_path,
				f.id AS file_id, COALESCE(f.duration, 0)::int AS duration,
				COALESCE(f.file_hash, '') AS file_hash, COALESCE(f.file_size, 0) AS file_size,
				f.duration > 0 OR f.probe_updated_at IS NOT NULL OR f.probe_failed_at IS NOT NULL AS probed,
				`+frameKeySQL+` AS frame_key
			FROM media_items mi
			JOIN media_files f ON f.content_id = mi.content_id
				AND f.episode_id IS NULL AND f.extra_id IS NULL AND f.missing_since IS NULL
			JOIN media_folders folder ON folder.id = f.media_folder_id AND folder.enabled
			WHERE mi.poster_path = ''
				AND mi.type = 'movie'
				AND (mi.poster_source_path = '' OR starts_with(mi.poster_source_path, $1))
				AND mi.status IS DISTINCT FROM 'pending'
				AND NOT (`+imagesLockedSQL("mi.locked_fields")+`)
			ORDER BY mi.content_id, f.id
		) c
		LEFT JOIN media_intro_fingerprints art
			ON art.media_file_id = c.file_id
			AND art.algorithm_version = $2
			AND art.config_hash = $3
			AND art.kind = $4
		WHERE c.probed
			AND c.frame_key IS NOT NULL
			AND NOT COALESCE(
				art.file_hash = c.file_hash
				AND art.file_size = c.file_size
				AND art.duration_seconds = c.duration
				AND (art.status = $5 OR (art.status = $6 AND art.retry_after > NOW() AND art.recorded_by = $7)),
				false)
		ORDER BY c.content_id
		LIMIT $8`,
		StillSourceFramePrefix, failures.AlgorithmVersion, failures.ConfigHash, failures.Kind,
		mediaartifact.StatusUnusable, mediaartifact.StatusFailed, node, limit)
	if err != nil {
		return nil, fmt.Errorf("query movies needing a frame poster: %w", err)
	}
	defer rows.Close()
	var pending []PendingFramePoster
	for rows.Next() {
		var p PendingFramePoster
		if err := rows.Scan(&p.ContentID, &p.PosterSource, &p.FileID, &p.Duration, &p.FrameKey); err != nil {
			return nil, fmt.Errorf("scan movie needing a frame poster: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate movies needing a frame poster: %w", err)
	}
	return pending, nil
}

// SetFramePosterIfSource records a poster taken from the item's video. It
// applies only while the poster slot is still empty with expectedSource as
// its source and the images are unlocked, so a sidecar, provider image,
// upload or admin pick that landed meanwhile is never overwritten; the frame
// is then left to the artwork revision GC. It reports whether it wrote.
func (r *ItemRepository) SetFramePosterIfSource(ctx context.Context, contentID, expectedSource, path, source, thumbhash string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_items
		SET poster_path = $3, poster_source_path = $4, poster_thumbhash = $5, updated_at = NOW()
		WHERE content_id = $1
			AND poster_path = ''
			AND poster_source_path = $2
			AND NOT (`+imagesLockedSQL("locked_fields")+`)`,
		contentID, expectedSource, path, source, thumbhash)
	if err != nil {
		return false, fmt.Errorf("set frame poster: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
