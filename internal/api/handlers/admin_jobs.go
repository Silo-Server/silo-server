package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

const adminJobDownloadExpiry = 15 * time.Minute

type AdminJobArtifactStore interface {
	Bucket() string
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
}

type adminJobRepository interface {
	List(ctx context.Context, opts adminjob.ListJobsOptions) ([]*models.AdminJob, error)
	GetByID(ctx context.Context, id string) (*models.AdminJob, error)
	Cancel(ctx context.Context, id, message string, expiresAt time.Time) (*models.AdminJob, error)
	CancelQueued(ctx context.Context, id, message string, expiresAt time.Time) (*models.AdminJob, error)
	UpdateProgress(ctx context.Context, id string, current, total int, message string) error
}

type AdminJobsHandler struct {
	repo           adminJobRepository
	store          AdminJobArtifactStore
	CancelRegistry *adminjob.CancelRegistry
	RealtimeHub    *notifications.Hub
	// ArtifactSigner mints self-authorizing download URLs for stores that
	// cannot presign. Nil leaves download_url empty, as before.
	ArtifactSigner *artworkurl.Signer
}

func NewAdminJobsHandler(repo adminJobRepository, store AdminJobArtifactStore) *AdminJobsHandler {
	return &AdminJobsHandler{repo: repo, store: store}
}

type adminJobResponse struct {
	ID                string          `json:"id"`
	JobType           string          `json:"job_type"`
	Status            string          `json:"status"`
	CreatedByUserID   int             `json:"created_by_user_id"`
	RequestPayload    json.RawMessage `json:"request_payload"`
	ResultPayload     json.RawMessage `json:"result_payload"`
	Message           string          `json:"message"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	ProgressCurrent   int             `json:"progress_current"`
	ProgressTotal     int             `json:"progress_total"`
	ArtifactSizeBytes int64           `json:"artifact_size_bytes"`
	PublicURL         string          `json:"public_url,omitempty"`
	RequestedAt       time.Time       `json:"requested_at"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	HeartbeatAt       *time.Time      `json:"heartbeat_at,omitempty"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
	PublishedAt       *time.Time      `json:"published_at,omitempty"`
	DownloadURL       string          `json:"download_url,omitempty"`
	DownloadExpiresAt *time.Time      `json:"download_expires_at,omitempty"`
}

type listAdminJobsResponse struct {
	Jobs []adminJobResponse `json:"jobs"`
}

type adminJobConflictResponse struct {
	Error       string            `json:"error"`
	Message     string            `json:"message"`
	ActiveJobID string            `json:"active_job_id,omitempty"`
	ActiveJob   *adminJobResponse `json:"active_job,omitempty"`
}

func adminJobToResponse(r *http.Request, job *models.AdminJob, store AdminJobArtifactStore) adminJobResponse {
	job = notifications.SafeStorageTransitionJob(job)
	resp := adminJobResponse{
		ID:                job.ID,
		JobType:           job.JobType,
		Status:            job.Status,
		CreatedByUserID:   job.CreatedByUserID,
		RequestPayload:    ensureJSONPayload(job.RequestPayload),
		ResultPayload:     ensureJSONPayload(job.ResultPayload),
		Message:           job.Message,
		ErrorMessage:      job.ErrorMessage,
		ProgressCurrent:   job.ProgressCurrent,
		ProgressTotal:     job.ProgressTotal,
		ArtifactSizeBytes: job.ArtifactSizeBytes,
		PublicURL:         job.PublicURL,
		RequestedAt:       job.RequestedAt,
		StartedAt:         job.StartedAt,
		CompletedAt:       job.CompletedAt,
		HeartbeatAt:       job.HeartbeatAt,
		ExpiresAt:         job.ExpiresAt,
		PublishedAt:       job.PublishedAt,
	}

	if store != nil && job.Status == adminjob.StatusCompleted && job.ArtifactBucket != "" && job.ArtifactKey != "" {
		expiresAt := time.Now().UTC().Add(adminJobDownloadExpiry)
		url, err := store.PresignGetURL(r.Context(), job.ArtifactBucket, job.ArtifactKey, adminJobDownloadExpiry)
		if err == nil {
			resp.DownloadURL = url
			resp.DownloadExpiresAt = &expiresAt
		}
	}

	return resp
}

func ensureJSONPayload(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage(`{}`)
	}
	return data
}
