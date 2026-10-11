package metadata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Queue operations for local sidecar artwork (file:// sources). Local art is
// copied by the process that discovered it, which is the process known to see
// the library mount; the queue keeps the job for retries.

const (
	// localImageCacheSourceSQL matches jobs whose source is a local sidecar.
	localImageCacheSourceSQL = "source_path ILIKE 'file://%'"

	// localArtworkDiscoveryHold is how long a job queued by the discovering
	// process stays out of other drains' reach while that process copies it.
	// Past the hold the job is an ordinary queued job any drain may retry, so a
	// process that dies or falls behind delays the copy by at most this long.
	localArtworkDiscoveryHold = 10 * time.Minute

	// imageCacheRootUnavailableRetry is how long a local job waits after a
	// claimer could not see the library root that holds the file. The attempt
	// is not counted: a server without the mount says nothing about the file,
	// and the job stays available to servers that have it.
	imageCacheRootUnavailableRetry = 10 * time.Minute
)

// ClaimDueLocal claims due jobs with local file:// sources only. Draining uses
// it while metadata.cache_images is off: that setting stops remote downloads,
// but local art is always copied.
func (r *ImageCacheJobRepository) ClaimDueLocal(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error) {
	if r == nil || r.pool == nil || limit <= 0 {
		return nil, nil
	}
	if err := r.recoverExpiredRunning(ctx, ""); err != nil {
		return nil, err
	}
	return r.claimDue(ctx, workerID, "", true, limit)
}

// enqueueHeldLocalJobs queues discovered local artwork behind
// localArtworkDiscoveryHold, so background drains on other servers leave it to
// the discovering process (claimQueuedLocalJob ignores the hold). It returns
// the slots left queued; a slot whose job already succeeded for the same
// source, or is running, needs no copy.
func (r *ImageCacheJobRepository) enqueueHeldLocalJobs(ctx context.Context, inputs []EnqueueImageCacheJobInput) ([]EnqueueImageCacheJobInput, error) {
	var queued []EnqueueImageCacheJobInput
	_, err := r.upsertImageCacheJobs(ctx, inputs, false, localArtworkDiscoveryHold, &queued)
	return queued, err
}

// claimQueuedLocalJob claims the queued job for one discovered slot when it
// still carries the discovered source, whatever its next_attempt_at. It
// returns nil when another worker holds the job, it already finished, or the
// slot's source has changed since.
func (r *ImageCacheJobRepository) claimQueuedLocalJob(ctx context.Context, in EnqueueImageCacheJobInput, workerID string) (*models.MetadataImageCacheJob, error) {
	if r == nil || r.pool == nil {
		return nil, nil
	}
	in, ok := normalizeImageCacheJobInput(in)
	if !ok {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE metadata_image_cache_jobs j
		SET status = 'running',
			locked_at = NOW(),
			locked_by = $6,
			updated_at = NOW()
		WHERE j.target_type = $1
		  AND j.target_content_id = $2
		  AND j.image_type = $3
		  AND j.target_language = $4
		  AND j.source_path = $5
		  AND j.status = 'queued'
		RETURNING `+imageCacheJobReturningColumns,
		in.TargetType, in.TargetContentID, in.ImageType, in.TargetLanguage, in.SourcePath, workerID)
	if err != nil {
		return nil, fmt.Errorf("claiming discovered local image cache job: %w", err)
	}
	jobs, err := scanClaimedImageCacheJobs(rows, 1)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return jobs[0], nil
}

// DeferClaimed returns a claimed job to the queue after delay without counting
// an attempt, recording why. The lease guard matches MarkFailed.
func (r *ImageCacheJobRepository) DeferClaimed(ctx context.Context, id int64, lockedBy string, errText string, delay time.Duration) error {
	if r == nil || r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = 'queued',
			next_attempt_at = NOW() + $3::interval,
			locked_at = NULL,
			locked_by = '',
			last_error = left($4, 2000),
			updated_at = NOW()
		WHERE id = $1
		  AND status = 'running'
		  AND locked_by = $2
	`, id, lockedBy, intervalLiteral(max(delay, 0)), errText)
	if err != nil {
		return fmt.Errorf("deferring metadata image cache job: %w", err)
	}
	return nil
}

// errNoLocalArtworkQueue reports a job store that cannot hold discovered
// local artwork for its discovering process.
var errNoLocalArtworkQueue = errors.New("metadata image cache does not support local artwork discovery")

// localArtworkJobStore is the queue surface the local artwork copier needs.
// ImageCacheJobRepository implements it; lightweight fakes may not.
type localArtworkJobStore interface {
	enqueueHeldLocalJobs(ctx context.Context, inputs []EnqueueImageCacheJobInput) ([]EnqueueImageCacheJobInput, error)
	claimQueuedLocalJob(ctx context.Context, in EnqueueImageCacheJobInput, workerID string) (*models.MetadataImageCacheJob, error)
}

// imageCacheJobDeferrer is implemented by job stores that can release a
// claimed job without counting an attempt.
type imageCacheJobDeferrer interface {
	DeferClaimed(ctx context.Context, id int64, lockedBy string, errText string, delay time.Duration) error
}

// localImageCacheJobClaimer is implemented by job stores that can claim only
// local jobs, which draining uses while remote caching is off.
type localImageCacheJobClaimer interface {
	ClaimDueLocal(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error)
}

// splitLocalImageCacheInputs separates local sidecar inputs from the rest.
func splitLocalImageCacheInputs(inputs []EnqueueImageCacheJobInput) (local, remote []EnqueueImageCacheJobInput) {
	for _, in := range inputs {
		if isLocalImageSourcePath(in.SourcePath) {
			local = append(local, in)
		} else {
			remote = append(remote, in)
		}
	}
	return local, remote
}
