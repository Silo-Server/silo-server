package notifications

import (
	"encoding/json"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	storageTransitionJobType        = "storage_transition"
	storageTransitionQueued         = "queued"
	storageTransitionRunning        = "running"
	storageTransitionCompleted      = "completed"
	storageTransitionUnknown        = "unknown"
	storageTransitionRestartPending = "restart_pending"
	storageTransitionCheckingTarget = "checking_target"
)

type storageTransitionEventResult struct {
	Phase                 string `json:"phase"`
	VerifiedObjects       int    `json:"verified_objects"`
	FailureCategory       string `json:"failure_category,omitempty"`
	ManualRestartRequired bool   `json:"manual_restart_required"`
}

// SafeStorageTransitionJob removes diagnostic storage details from an outbound
// administrator job. The original row keeps its diagnostic detail.
func SafeStorageTransitionJob(job *models.AdminJob) *models.AdminJob {
	if job == nil || job.JobType != storageTransitionJobType {
		return job
	}
	var raw struct {
		Phase                 string `json:"phase"`
		VerifiedObjects       int    `json:"verified_objects"`
		CopiedObjects         int    `json:"copied_objects"`
		ClaimGeneration       int64  `json:"claim_generation"`
		FailureCategory       string `json:"failure_category"`
		ManualRestartRequired bool   `json:"manual_restart_required"`
	}
	_ = json.Unmarshal(job.ResultPayload, &raw)
	result := storageTransitionEventResult{
		VerifiedObjects:       max(raw.VerifiedObjects, raw.CopiedObjects, 0),
		ManualRestartRequired: raw.ManualRestartRequired,
	}
	switch job.Status {
	case storageTransitionQueued:
		result.Phase = storageTransitionQueued
		result.VerifiedObjects = 0
		result.ManualRestartRequired = false
	case "failed":
		result.Phase = "failed"
		result.ManualRestartRequired = false
		switch raw.FailureCategory {
		case "preparation_failed", "target_check_failed", "copy_failed", "verification_failed", "commit_failed":
			result.FailureCategory = raw.FailureCategory
		default:
			result.FailureCategory = storageTransitionUnknown
		}
	case "cancelled": //nolint:misspell // Preserve the stored admin job status.
		result.Phase = "canceled"
		result.ManualRestartRequired = false
	case storageTransitionCompleted:
		if raw.Phase == storageTransitionRestartPending || raw.ManualRestartRequired {
			result.Phase = storageTransitionRestartPending
		} else {
			result.Phase = storageTransitionCompleted
		}
	case storageTransitionRunning:
		// A new claim has not reported progress yet. Older receipts without a
		// claim number are current only for the first claim.
		if raw.ClaimGeneration != job.ClaimGeneration && (raw.ClaimGeneration != 0 || job.ClaimGeneration > 1) {
			result.Phase = storageTransitionCheckingTarget
			result.VerifiedObjects = 0
			result.ManualRestartRequired = false
			break
		}
		switch raw.Phase {
		case storageTransitionCheckingTarget, "copying", "verifying", "committing", storageTransitionRestartPending:
			result.Phase = raw.Phase
		default:
			result.Phase = storageTransitionCheckingTarget
			result.VerifiedObjects = 0
			result.ManualRestartRequired = false
		}
	default:
		result.Phase = storageTransitionCheckingTarget
		result.VerifiedObjects = 0
		result.ManualRestartRequired = false
	}
	resultPayload, _ := json.Marshal(result)
	return &models.AdminJob{
		ID: job.ID, JobType: job.JobType, Status: job.Status,
		CreatedByUserID: job.CreatedByUserID,
		RequestPayload:  json.RawMessage(`{}`), ResultPayload: resultPayload,
		ProgressCurrent: result.VerifiedObjects,
		RequestedAt:     job.RequestedAt, StartedAt: job.StartedAt, CompletedAt: job.CompletedAt,
		HeartbeatAt: job.HeartbeatAt, ExpiresAt: job.ExpiresAt, UpdatedAt: job.UpdatedAt,
	}
}
