package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
)

// JobKind identifies what an AI subtitle job does.
type JobKind string

const (
	// JobKindTranslate translates an existing text subtitle track. SourceIndex
	// is the combined player subtitle index of the source track.
	JobKindTranslate JobKind = "translate"
	// JobKindTranscribe generates a subtitle track from an audio track via
	// Whisper ASR. SourceIndex is the 0-based audio track index (-1 = default
	// track); TargetLanguage is optional and acts as a language hint.
	JobKindTranscribe JobKind = "transcribe"
	// JobKindTranscribeTranslate chains transcription with LLM translation to
	// TargetLanguage, storing both the transcript and the translated track.
	JobKindTranscribeTranslate JobKind = "transcribe_translate"
)

// IsTranscribe reports whether the kind starts from audio.
func (k JobKind) IsTranscribe() bool {
	return k == JobKindTranscribe || k == JobKindTranscribeTranslate
}

// JobStatus is the lifecycle state of a job, shared with the other AI job
// services via jobrunner.
type JobStatus = jobrunner.Status

const (
	JobStatusPending   = jobrunner.StatusPending
	JobStatusRunning   = jobrunner.StatusRunning
	JobStatusCompleted = jobrunner.StatusCompleted
	JobStatusFailed    = jobrunner.StatusFailed
	JobStatusCancelled = jobrunner.StatusCancelled
)

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
	// QuotaExempt skips the per-user transcription quota. The HTTP layer
	// decides exemption policy (the household parent of an admin account
	// manages the budget, so it is not bound by it); the service enforces.
	QuotaExempt bool
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
