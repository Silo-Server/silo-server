package apiv2

import (
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The admin jobs domain. Only the job resource lives here so far: the
// library operations that queue work answer with it (deleteLibrary). The
// admin-jobs section adds the job operations to this file.

// AdminJob is one unit of queued administrative work and its progress.
type AdminJob struct {
	ID                string          `json:"id" doc:"Job identifier" example:"01J9Z8C3W4R5T6Y7U8I9O0P1Q2"`
	JobType           string          `json:"job_type" doc:"What the job does; the request payload's shape follows it" example:"delete_library"`
	Status            string          `json:"status" doc:"Lifecycle state" example:"queued"`
	CreatedByUserID   ID              `json:"created_by_user_id" doc:"The account that queued the job" example:"2"`
	RequestPayload    json.RawMessage `json:"request_payload" doc:"Job-type-specific request document; {} when the type carries none"`
	ResultPayload     json.RawMessage `json:"result_payload" doc:"Job-type-specific result document; {} until the job completes"`
	Message           string          `json:"message" doc:"Operator-facing status line" example:"Queued library deletion"`
	ErrorMessage      string          `json:"error_message,omitempty" doc:"Failure reason; absent unless the job failed"`
	ProgressCurrent   int             `json:"progress_current" example:"0"`
	ProgressTotal     int             `json:"progress_total" doc:"0 when the job has no measurable progress" example:"0"`
	ArtifactSizeBytes int64           `json:"artifact_size_bytes" doc:"Size of the produced artifact; 0 when none" example:"0"`
	PublicURL         string          `json:"public_url,omitempty" doc:"Public URL of a published artifact; absent until published"`
	RequestedAt       Instant         `json:"requested_at" example:"2026-01-02T03:04:05.678Z"`
	StartedAt         *Instant        `json:"started_at,omitempty" doc:"Absent until a worker picks the job up"`
	CompletedAt       *Instant        `json:"completed_at,omitempty" doc:"Absent until the job finishes"`
	HeartbeatAt       *Instant        `json:"heartbeat_at,omitempty" doc:"Last worker heartbeat; absent before the first"`
	ExpiresAt         *Instant        `json:"expires_at,omitempty" doc:"When the artifact expires; absent when it never does"`
	PublishedAt       *Instant        `json:"published_at,omitempty" doc:"Absent until the artifact is published"`
}

// adminJobOf renders a job without artifact presigning, the same view the
// v1 library endpoints answer with when they queue work.
func adminJobOf(job *models.AdminJob) AdminJob {
	return AdminJob{
		ID:                job.ID,
		JobType:           job.JobType,
		Status:            job.Status,
		CreatedByUserID:   IDFromInt(int64(job.CreatedByUserID)),
		RequestPayload:    jsonDocument(job.RequestPayload),
		ResultPayload:     jsonDocument(job.ResultPayload),
		Message:           job.Message,
		ErrorMessage:      job.ErrorMessage,
		ProgressCurrent:   job.ProgressCurrent,
		ProgressTotal:     job.ProgressTotal,
		ArtifactSizeBytes: job.ArtifactSizeBytes,
		PublicURL:         job.PublicURL,
		RequestedAt:       NewInstant(job.RequestedAt),
		StartedAt:         instantPtr(job.StartedAt),
		CompletedAt:       instantPtr(job.CompletedAt),
		HeartbeatAt:       instantPtr(job.HeartbeatAt),
		ExpiresAt:         instantPtr(job.ExpiresAt),
		PublishedAt:       instantPtr(job.PublishedAt),
	}
}

// jsonDocument is an empty object where the store holds no document, so the
// member is always a JSON value.
func jsonDocument(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// acceptedJob renders a queued job with its Location. The job read lives at
// /admin/jobs/{id}, the resource the admin-tasks section owns.
func acceptedJob(job *models.AdminJob) *AdminJobAcceptedOutput {
	return &AdminJobAcceptedOutput{Location: Prefix + "/admin/jobs/" + job.ID, Body: adminJobOf(job)}
}
