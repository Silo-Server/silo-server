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
// image on its way, each with one file: the lowest-id live main file with a
// known duration (the frame position depends on it) and no failure under key
// that blocks it. A sidecar or provider poster waiting for its copy keeps a
// source of its own, so the movie is not listed; a frame whose copy the
// artwork reconciler cleared keeps its frame source, so it is listed again. A
// movie waits while it is pending its first match, while its images are
// locked, and while none of its files in an enabled library qualifies. A
// movie with live main files in more than one library is never listed,
// whatever state those files are in: its poster is shown in every library
// the movie is in, and a frame from one library's file would show a viewer
// of another library a file they may not see. A failure blocks a file while the file is unchanged: for good when it cannot
// be decoded, otherwise until its retry time, whichever server recorded it.
//
// The empty poster_path is an equality probe on idx_media_items_poster_path_gc,
// so the scan reads only the items with no poster, and each of them joins its
// files through the content_id index and their artifacts through the primary
// key. The other-library check probes idx_media_files_active_content_folder
// (content_id, media_folder_id WHERE missing_since IS NULL).
func (r *ItemRepository) ListPendingFramePosters(ctx context.Context, failures mediaartifact.Key, limit int) ([]PendingFramePoster, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (mi.content_id)
			mi.content_id, mi.poster_source_path, f.id, f.duration::int, `+frameKeySQL+`
		FROM media_items mi
		JOIN media_files f ON f.content_id = mi.content_id
			AND f.episode_id IS NULL AND f.extra_id IS NULL AND f.missing_since IS NULL
			AND f.duration > 0 AND f.file_size IS NOT NULL
		JOIN media_folders folder ON folder.id = f.media_folder_id AND folder.enabled
		LEFT JOIN media_intro_fingerprints art
			ON art.media_file_id = f.id
			AND art.algorithm_version = $2
			AND art.config_hash = $3
			AND art.kind = $4
		WHERE mi.poster_path = ''
			AND mi.type = 'movie'
			AND (mi.poster_source_path = '' OR starts_with(mi.poster_source_path, $1))
			AND mi.status IS DISTINCT FROM 'pending'
			AND NOT (`+imagesLockedSQL("mi.locked_fields")+`)
			AND NOT EXISTS (
				SELECT 1 FROM media_files other
				WHERE other.content_id = mi.content_id
					AND other.missing_since IS NULL
					AND other.episode_id IS NULL AND other.extra_id IS NULL
					AND other.media_folder_id IS DISTINCT FROM f.media_folder_id
			)
			AND NOT COALESCE(
				art.file_hash = COALESCE(f.file_hash, '')
				AND art.file_size = f.file_size
				AND art.duration_seconds = f.duration
				AND (art.status = $5 OR (art.status = $6 AND art.retry_after > NOW())),
				false)
		ORDER BY mi.content_id, f.id
		LIMIT $7`,
		StillSourceFramePrefix, failures.AlgorithmVersion, failures.ConfigHash, failures.Kind,
		mediaartifact.StatusUnusable, mediaartifact.StatusFailed, limit)
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
