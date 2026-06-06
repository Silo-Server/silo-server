package models

import (
	"encoding/json"
	"time"
)

// AdminJob represents a reusable background job row for admin workflows.
type AdminJob struct {
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
	ArtifactBucket    string          `json:"-"`
	ArtifactKey       string          `json:"-"`
	ArtifactSizeBytes int64           `json:"artifact_size_bytes"`
	PublicURL         string          `json:"public_url,omitempty"`
	RequestedAt       time.Time       `json:"requested_at"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	HeartbeatAt       *time.Time      `json:"heartbeat_at,omitempty"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
	PublishedAt       *time.Time      `json:"published_at,omitempty"`
	UpdatedAt         time.Time       `json:"updated_at"`
}
