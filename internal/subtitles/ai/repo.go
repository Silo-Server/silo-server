package ai

import (
	"context"
	"time"
)

// JobRepository persists AI subtitle jobs.
type JobRepository interface {
	// InsertJob inserts a new job. When quota is non-nil it enforces the
	// per-user transcription cap atomically with the insert (serialized per
	// user), returning a *QuotaExceededError instead of inserting once the
	// user has quota.Limit countable jobs since quota.Since.
	InsertJob(ctx context.Context, job *Job, quota *JobQuota) error
	GetJob(ctx context.Context, id int64) (*Job, error)
	// GetActiveJobByIdempotencyKey returns a pending/running job with the given
	// key, or nil if none exists.
	GetActiveJobByIdempotencyKey(ctx context.Context, key string) (*Job, error)
	ListJobsByMediaFile(ctx context.Context, mediaFileID int) ([]Job, error)
	// CountTranscribeJobsByUserSince counts the user's quota-consuming
	// transcription jobs (transcribe / transcribe_translate) created at or
	// after `since`. Failed or cancelled jobs that never produced any
	// transcription work are excluded, so server-side faults don't lock the
	// user out; jobs that burned ASR compute count even if they later failed.
	CountTranscribeJobsByUserSince(ctx context.Context, userID int, since time.Time) (int, error)
	UpdateProgress(ctx context.Context, id int64, status JobStatus, progress float64, message string) error
	CompleteJob(ctx context.Context, id int64, subtitleID int) error
	FailJob(ctx context.Context, id int64, status JobStatus, message string) error
	Heartbeat(ctx context.Context, id int64) error
	// ResetStaleJobs marks pending/running jobs whose heartbeat predates `before`
	// as failed with the given message, clearing jobs orphaned by a crashed
	// worker while leaving alone a job a live worker is still heartbeating.
	// Returns the number of rows reset.
	ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error)
}
