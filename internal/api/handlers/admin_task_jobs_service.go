package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// AdminJobArtifactDownload is one job artifact opened for streaming. It lives
// here, not in apiv2, because apiv2 imports this package.
type AdminJobArtifactDownload struct {
	Filename string
	Size     *int64
	Body     io.ReadCloser
}

// artifactStreamer is the optional streaming read a store offers. The S3
// client has it; so does the local BucketAPI. A store without it simply cannot
// serve the streaming route, and the signed URL is never minted for it.
type artifactStreamer interface {
	GetObjectStream(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

// ErrJobArtifactNotFound reports that a job or its artifact does not exist, as
// distinct from storage being unreachable. Callers answer 404 for this and a
// service error for anything else, so an outage does not read to an authorized
// administrator as a permanently missing download.
var ErrJobArtifactNotFound = errors.New("job artifact not found")

// OpenAdminJobArtifact streams a completed job's artifact. The caller has
// already verified the signed capability for this job ID; authorization does
// not happen here.
func (h *AdminJobsHandler) OpenAdminJobArtifact(ctx context.Context, id string) (AdminJobArtifactDownload, error) {
	if h == nil || h.repo == nil || h.store == nil {
		return AdminJobArtifactDownload{}, fmt.Errorf("admin job artifacts are not configured")
	}
	job, err := h.repo.GetByID(ctx, id)
	if errors.Is(err, adminjob.ErrJobNotFound) {
		return AdminJobArtifactDownload{}, ErrJobArtifactNotFound
	}
	if err != nil {
		return AdminJobArtifactDownload{}, err
	}
	if job.Status != adminjob.StatusCompleted || job.ArtifactBucket == "" || job.ArtifactKey == "" {
		return AdminJobArtifactDownload{}, fmt.Errorf("%w: job %s", ErrJobArtifactNotFound, id)
	}
	streamer, ok := h.store.(artifactStreamer)
	if !ok {
		return AdminJobArtifactDownload{}, fmt.Errorf("artifact storage cannot stream")
	}
	body, err := streamer.GetObjectStream(ctx, job.ArtifactBucket, job.ArtifactKey)
	if errors.Is(err, blobstore.ErrNotFound) || errors.Is(err, s3client.ErrNotFound) {
		// Cleanup or an operator removed the object behind a retained job. That
		// is permanent, not an outage.
		return AdminJobArtifactDownload{}, fmt.Errorf("%w: job %s: %w", ErrJobArtifactNotFound, id, err)
	}
	if err != nil {
		return AdminJobArtifactDownload{}, err
	}
	download := AdminJobArtifactDownload{Body: body, Filename: path.Base(job.ArtifactKey)}
	if job.ArtifactSizeBytes > 0 {
		download.Size = &job.ArtifactSizeBytes
	}
	return download, nil
}

// AdminTaskJobPublicLinkSupported reports whether this server can mint a
// seven-day public link for an export. Only storage-side presigning produces a
// URL usable outside this server, so a local store cannot, and the UI hides the
// action rather than offering one that always fails. A store that says nothing
// about presigning is assumed to support it, which keeps S3 unchanged.
func (h *AdminJobsHandler) AdminTaskJobPublicLinkSupported() bool {
	if h == nil || h.store == nil {
		return false
	}
	if capability, ok := h.store.(interface{ SupportsPresign() bool }); ok {
		return capability.SupportsPresign()
	}
	return true
}

// ListAdminTaskJobs uses repository pagination without exposing legacy HTTP DTOs.
func (h *AdminJobsHandler) ListAdminTaskJobs(ctx context.Context, kind string, before time.Time, id string, limit int) ([]*models.AdminJob, error) {
	repo, ok := h.repo.(interface {
		ListPage(context.Context, string, time.Time, string, int) ([]*models.AdminJob, error)
	})
	if !ok {
		return nil, fmt.Errorf("paged admin jobs unavailable")
	}
	return repo.ListPage(ctx, kind, before, id, limit)
}
func (h *AdminJobsHandler) GetAdminTaskJob(ctx context.Context, id string) (*models.AdminJob, error) {
	return h.repo.GetByID(ctx, id)
}
func (h *AdminJobsHandler) RequestAdminTaskJobCancellation(ctx context.Context, id string) (*models.AdminJob, error) {
	repo, ok := h.repo.(interface {
		RequestCancellation(context.Context, string) (*models.AdminJob, error)
	})
	if !ok {
		return nil, fmt.Errorf("admin job cancellation unavailable")
	}
	job, err := repo.RequestCancellation(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.JobType != adminjob.JobTypeImageCacheCleanup {
		return job, nil
	}
	switch job.Status {
	case adminjob.StatusRunning:
		// The flag reaches a cleanup running on any node at its next cancel
		// poll. When it runs in this process, stop it now instead of one poll
		// later.
		if h.CancelRegistry != nil {
			h.CancelRegistry.Cancel(job.ID)
		}
	case adminjob.StatusQueued:
		// A queued cleanup, new or yielded between slices, holds nothing to
		// release, so end it now rather than when a runner next claims it,
		// which can be hours away behind other cleanups. If a runner claims it
		// first, the claim sees the flag and ends it.
		canceled, cancelErr := h.repo.CancelQueued(ctx, job.ID,
			adminjob.QueuedImageCacheCleanupCanceledMessage(job.ProgressCurrent, job.ProgressTotal),
			time.Now().UTC().Add(7*24*time.Hour))
		if cancelErr != nil {
			if !errors.Is(cancelErr, adminjob.ErrJobNotCancellable) {
				slog.WarnContext(ctx, "admin jobs: failed to cancel queued image cache cleanup", "component", "api", "job_id", job.ID, "error", cancelErr)
				return job, nil
			}
			// A runner claimed the job first. Report its current state: the
			// claim may already have ended it from the flag.
			if advanced, loadErr := h.repo.GetByID(ctx, job.ID); loadErr == nil {
				return advanced, nil
			}
			return job, nil
		}
		if h.RealtimeHub != nil {
			publishEventJob(ctx, h.RealtimeHub.EventsHub(), string(notifications.TypeJobCancelled), canceled)
		}
		return canceled, nil
	}
	return job, nil
}
func (h *AdminJobsHandler) AdminTaskJobDownload(ctx context.Context, job *models.AdminJob) (string, *time.Time) {
	if h.store == nil || job.Status != adminjob.StatusCompleted || job.ArtifactBucket == "" || job.ArtifactKey == "" {
		return "", nil
	}
	expiry := time.Now().UTC().Add(adminJobDownloadExpiry)
	url, err := h.store.PresignGetURL(ctx, job.ArtifactBucket, job.ArtifactKey, adminJobDownloadExpiry)
	if err == nil {
		return url, &expiry
	}
	// A local store cannot presign. The browser opens this URL in a new tab and
	// sends no Authorization header, so the replacement must authorize itself
	// the same way a presigned S3 URL did: a signed, object-scoped capability
	// pointing at the streaming artifact route.
	if h.ArtifactSigner == nil {
		return "", nil
	}
	signed, expires := h.ArtifactSigner.SignFor(job.ID, time.Now(), adminJobDownloadExpiry)
	return signed, &expires
}
