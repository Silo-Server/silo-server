package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// JobKind identifies what an AI subtitle job does. Only translation ships in
// the first iteration; transcription kinds are reserved for the Whisper ASR
// follow-up so the schema and API do not change again.
type JobKind string

const (
	JobKindTranslate JobKind = "translate"
)

// JobStatus is the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// Terminal reports whether a status is final.
func (s JobStatus) Terminal() bool {
	switch s {
	case JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
		return true
	default:
		return false
	}
}

// Job is a persisted AI subtitle job. It is serialized to the API as-is.
type Job struct {
	ID               int64     `json:"id"`
	MediaFileID      int       `json:"media_file_id"`
	Kind             JobKind   `json:"kind"`
	SourceIndex      int       `json:"source_index"`
	SourceLanguage   string    `json:"source_language"`
	TargetLanguage   string    `json:"target_language"`
	Engine           string    `json:"engine"`
	Model            string    `json:"model"`
	Status           JobStatus `json:"status"`
	Progress         float64   `json:"progress"`
	ProgressMessage  string    `json:"progress_message"`
	ResultSubtitleID *int      `json:"result_subtitle_id"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	IdempotencyKey   string    `json:"-"`
	RequestedBy      *int      `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	HeartbeatAt      time.Time `json:"-"`

	// Transient, set from the request and used only while the job runs (not
	// persisted): the realtime session to stream live cues to, and the playhead
	// position so translation starts where the viewer is watching.
	SessionID     string  `json:"-"`
	StartPosition float64 `json:"-"`
}

// JobRequest is the input to Service.Enqueue.
type JobRequest struct {
	MediaFileID    int
	Kind           JobKind
	SourceIndex    int
	SourceLanguage string
	TargetLanguage string
	RequestedBy    *int
	// SessionID, when set, streams live cues to that playback session.
	SessionID string
	// StartPosition (seconds) makes translation start at the viewer's playhead.
	StartPosition float64
}

// idempotencyKey derives the dedup key for a job. Two requests for the same
// source track, target language, and model collapse to one in-flight job.
func idempotencyKey(mediaFileID int, kind JobKind, sourceIndex int, targetLang, model string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d|%s|%s", mediaFileID, kind, sourceIndex, targetLang, model)))
	return hex.EncodeToString(sum[:])
}
