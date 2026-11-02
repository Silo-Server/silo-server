package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Queue operations for local sidecar artwork (file:// sources). Local art is
// copied by the process that discovered it, which is the process known to see
// the library mount; the queue keeps the job for retries.

const (
	// localImageCacheSourceSQL matches jobs whose source is a local sidecar
	// file (LocalImageSourceURL writes the scheme in lower case).
	localImageCacheSourceSQL = "source_path LIKE 'file://%'"

	// heldLocalDiscoverySQL matches a local job queued by a discovery and not
	// tried yet: no failed attempt and no deferral. A manual refresh of its
	// target claims it whatever its hold, and does not make it due for
	// other drains.
	heldLocalDiscoverySQL = "(" + localImageCacheSourceSQL + " AND attempt_count = 0 AND last_error = '')"

	// localArtworkDiscoveryHold is how long a job queued by the discovering
	// process stays out of other drains' reach while that process copies it.
	// Past the hold the job is an ordinary queued job any drain may retry, so a
	// process that dies or falls behind delays the copy by at most this long.
	localArtworkDiscoveryHold = 10 * time.Minute

	// imageCacheRootUnavailableRetry is how long a local job waits after a
	// claimer could not see the storage that holds the file. The attempt is not
	// counted: a server without the mount says nothing about the file, and the
	// job stays available to servers that have it. A job deferred again for the
	// same reason waits imageCacheRootUnavailableRepeatRetry, so storage that no
	// server mounts any more costs a claim an hour per job, not six.
	imageCacheRootUnavailableRetry       = 10 * time.Minute
	imageCacheRootUnavailableRepeatRetry = time.Hour

	// localImageStorageUnavailable starts the last_error of a deferred job.
	localImageStorageUnavailable = "local image storage not mounted on this server"
)

// enqueueHeldLocalJobs queues discovered local artwork behind
// localArtworkDiscoveryHold, so background drains on other servers leave it to
// the discovering process (claimQueuedLocalJob ignores the hold). It returns
// the slots left queued, including those of chunks committed before an error;
// a slot whose job already succeeded for the same source, or is running, needs
// no copy.
func (r *ImageCacheJobRepository) enqueueHeldLocalJobs(ctx context.Context, inputs []EnqueueImageCacheJobInput) ([]EnqueueImageCacheJobInput, error) {
	var queued []EnqueueImageCacheJobInput
	_, err := r.upsertImageCacheJobs(ctx, inputs, false, localArtworkDiscoveryHold, &queued)
	return queued, err
}

// claimQueuedLocalJob claims the queued job for one discovered slot when it
// still carries the discovered source. It ignores the discovery hold and a
// deferral for missing storage (also one that followed an earlier failed
// attempt), but not the backoff of an attempt that failed. It returns nil
// when another worker holds the job, it already finished or is backing off,
// or the slot's source has changed since.
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
		  AND (j.attempt_count = 0 OR j.next_attempt_at <= NOW() OR starts_with(j.last_error, $7))
		RETURNING `+imageCacheJobReturningColumns,
		in.TargetType, in.TargetContentID, in.ImageType, in.TargetLanguage, in.SourcePath, workerID, localImageStorageUnavailable)
	if err != nil {
		return nil, fmt.Errorf("claiming discovered local image cache job: %w", err)
	}
	jobs, err := scanClaimedImageCacheJobs(rows, 1)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return jobs[0], nil
}

// DeferClaimed returns a claimed job to the queue without counting an attempt,
// recording why. It waits delay, or repeatDelay when the job was due and last
// deferred for the same reason. A job claimed before it was due (a discovery
// copy ignores a deferral) keeps its wait, extended to delay at most, so a
// server without the mount that keeps re-finding the job cannot push it
// further from servers that have it. The lease guard matches MarkFailed.
func (r *ImageCacheJobRepository) DeferClaimed(ctx context.Context, id int64, lockedBy string, errText string, delay, repeatDelay time.Duration) error {
	if r == nil || r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = 'queued',
			next_attempt_at = GREATEST(next_attempt_at, NOW() + CASE
				WHEN last_error = left($4, 2000) AND next_attempt_at <= NOW() THEN $5::interval
				ELSE $3::interval
			END),
			locked_at = NULL,
			locked_by = '',
			last_error = left($4, 2000),
			updated_at = NOW()
		WHERE id = $1
		  AND status = 'running'
		  AND locked_by = $2
	`, id, lockedBy, intervalLiteral(max(delay, 0)), errText, intervalLiteral(max(repeatDelay, 0)))
	if err != nil {
		return fmt.Errorf("deferring metadata image cache job: %w", err)
	}
	return nil
}

// targetHasDeferredLocalJobs reports whether a job in the target's scope is
// waiting for a server with its storage mounted (DeferClaimed), so a manual
// refresh does not report its artwork as done.
func (r *ImageCacheJobRepository) targetHasDeferredLocalJobs(ctx context.Context, targetContentID string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, nil
	}
	var deferred bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM metadata_image_cache_jobs
			WHERE `+imageCacheTargetScope("$1")+`
			  AND status = 'queued'
			  AND next_attempt_at > NOW()
			  AND starts_with(last_error, $2)
		)
	`, strings.TrimSpace(targetContentID), localImageStorageUnavailable).Scan(&deferred); err != nil {
		return false, fmt.Errorf("checking deferred local image cache jobs: %w", err)
	}
	return deferred, nil
}

// errNoLocalArtworkQueue reports a job store that cannot hold discovered
// local artwork for its discovering process.
var errNoLocalArtworkQueue = errors.New("metadata image cache does not support local artwork discovery")

// errLocalArtworkCachingOff refuses a discovery copy while
// metadata.cache_images is off.
var errLocalArtworkCachingOff = errors.New("metadata image caching is off")

// localArtworkJobStore is the queue surface the local artwork copier needs.
// ImageCacheJobRepository implements it; lightweight fakes may not.
type localArtworkJobStore interface {
	enqueueHeldLocalJobs(ctx context.Context, inputs []EnqueueImageCacheJobInput) ([]EnqueueImageCacheJobInput, error)
	claimQueuedLocalJob(ctx context.Context, in EnqueueImageCacheJobInput, workerID string) (*models.MetadataImageCacheJob, error)
}

// imageCacheJobDeferrer is implemented by job stores that can release a
// claimed job without counting an attempt.
type imageCacheJobDeferrer interface {
	DeferClaimed(ctx context.Context, id int64, lockedBy string, errText string, delay, repeatDelay time.Duration) error
}

// isLocalSidecarSource reports whether source names a sidecar file (file://):
// the artwork copied on discovery. It matches localImageCacheSourceSQL.
func isLocalSidecarSource(source string) bool {
	return strings.HasPrefix(strings.TrimSpace(source), "file://")
}

// splitLocalImageCacheInputs separates local sidecar file inputs from the rest.
func splitLocalImageCacheInputs(inputs []EnqueueImageCacheJobInput) (local, remote []EnqueueImageCacheJobInput) {
	for _, in := range inputs {
		if isLocalSidecarSource(in.SourcePath) {
			local = append(local, in)
		} else {
			remote = append(remote, in)
		}
	}
	return local, remote
}
