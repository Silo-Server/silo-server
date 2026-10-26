package scanner

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// PrepareMarkerThumbnailState registers opaque image identities for an on-demand
// lookup. Provider ranges remain ephemeral. Registering the inventory fences a
// worker whose provider snapshot has since been superseded.
func (r *FileRepository) PrepareMarkerThumbnailState(ctx context.Context, expected *models.MediaFile) (*models.MediaFile, error) {
	return r.saveMarkerThumbnailState(ctx, expected, nil, true)
}

// UpdateMarkerThumbnailState fences extraction against canonical edits and the
// latest on-demand inventory under the marker writer's row lock.
func (r *FileRepository) UpdateMarkerThumbnailState(ctx context.Context, expected *models.MediaFile, thumbnails []models.MarkerThumbnail) (*models.MediaFile, error) {
	return r.saveMarkerThumbnailState(ctx, expected, thumbnails, false)
}

func (r *FileRepository) saveMarkerThumbnailState(ctx context.Context, expected *models.MediaFile, thumbnails []models.MarkerThumbnail, prepare bool) (*models.MediaFile, error) {
	var tx pgx.Tx
	var err error
	if lock, ok := ctx.Value(chapterThumbnailLockKey{}).(chapterThumbnailLock); ok && lock.repo == r && lock.fileID == expected.ID {
		tx, err = lock.conn.Begin(ctx)
	} else {
		tx, err = r.pool.Begin(ctx)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var enabled bool
	// Match library deletion's lock order.
	err = tx.QueryRow(ctx, `SELECT enabled AND chapter_thumbnails_enabled FROM media_folders WHERE id=$1 FOR SHARE`, expected.MediaFolderID).Scan(&enabled)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	current, err := scanMediaFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM media_files WHERE id=$1 FOR UPDATE`, expected.ID))
	if err != nil {
		return nil, err
	}
	base := models.EffectiveMarkerSegments(expected)
	if expected.MarkerThumbnailBaseSegments != nil {
		base = expected.MarkerThumbnailBaseSegments
	}
	if models.MarkerFileIdentity(current) != models.MarkerFileIdentity(expected) || current.FilePath != expected.FilePath ||
		!slices.Equal(models.EffectiveMarkerSegments(current), base) || current.MissingSince != nil || current.MediaFolderID != expected.MediaFolderID {
		return nil, nil
	}
	overlay := *expected
	overlay.MarkerThumbnails = current.MarkerThumbnails
	if prepare {
		thumbnails = models.EffectiveMarkerThumbnails(&overlay)
		for i := range thumbnails {
			// This lookup supplies the range the deferral was waiting for.
			if thumbnails[i].ThumbnailLastError == models.MarkerSnapshotExpired {
				thumbnails[i].ThumbnailRetryAfter, thumbnails[i].ThumbnailFailedAt, thumbnails[i].ThumbnailLastError = nil, nil, ""
			}
		}
	} else if expected.MarkerThumbnailBaseSegments != nil {
		candidates := models.EffectiveMarkerThumbnails(expected)
		if len(candidates) != len(current.MarkerThumbnails) {
			return nil, nil
		}
		for i := range candidates {
			if candidates[i].Identity != current.MarkerThumbnails[i].Identity {
				return nil, nil
			}
		}
	} else {
		// A canonical background job must preserve opaque on-demand image references.
		merged := slices.Clone(current.MarkerThumbnails)
		for _, thumbnail := range thumbnails {
			i := slices.IndexFunc(merged, func(old models.MarkerThumbnail) bool { return old.Identity == thumbnail.Identity })
			if i < 0 {
				// A lookup may have registered a different effective inventory while
				// this canonical worker was extracting on another replica.
				if len(current.MarkerThumbnails) > 0 {
					return nil, nil
				}
				merged = append(merged, thumbnail)
			} else {
				merged[i] = thumbnail
			}
		}
		thumbnails = merged
	}
	// Image state deliberately contains no provider kind, range, or provenance.
	type imageState struct {
		Identity   string     `json:"identity"`
		Path       string     `json:"thumbnail_path,omitempty"`
		Hash       string     `json:"thumbnail_thumbhash,omitempty"`
		RetryAfter *time.Time `json:"thumbnail_retry_after,omitempty"`
		FailedAt   *time.Time `json:"thumbnail_failed_at,omitempty"`
		LastError  string     `json:"thumbnail_last_error,omitempty"`
	}
	states := make([]imageState, 0, len(thumbnails))
	for _, image := range thumbnails {
		states = append(states, imageState{image.Identity, image.ThumbnailPath, image.ThumbnailThumbhash, image.ThumbnailRetryAfter, image.ThumbnailFailedAt, image.ThumbnailLastError})
	}
	data, err := json.Marshal(states)
	if err != nil {
		return nil, err
	}
	saved, err := scanMediaFile(tx.QueryRow(ctx, `UPDATE media_files SET marker_thumbnails=$2, updated_at=NOW() WHERE id=$1 RETURNING `+fileColumns, current.ID, data))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if expected.MarkerThumbnailBaseSegments != nil {
		overlay.MarkerThumbnails = saved.MarkerThumbnails
		return &overlay, nil
	}
	return saved, nil
}
