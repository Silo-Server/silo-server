// Package translation translates catalog descriptions (overviews, taglines)
// into the localization tables with an OpenAI-compatible chat model, via the
// shared AI core. Jobs are triggered manually from the metadata editor or
// automatically after a metadata refresh when the library opts in and the
// providers had no localization for its language. Results are ordinary
// localization rows (marked source='ai'), so every client receives them
// through the existing localized detail responses with no client changes.
package translation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
)

var (
	// ErrNotConfigured is returned when metadata translation is requested but
	// the feature is disabled or the AI endpoint is missing required settings.
	ErrNotConfigured = errors.New("metadata AI translation is not configured")
	// ErrInvalidRequest wraps caller-input validation failures so handlers can
	// map them to 400 rather than 500.
	ErrInvalidRequest = errors.New("invalid metadata translation request")
	// ErrJobNotFound is returned for unknown job IDs.
	ErrJobNotFound = errors.New("metadata translation job not found")
)

// TargetKind identifies what a job translates.
type TargetKind string

const (
	// TargetItem covers a movie or series; for series, IncludeChildren expands
	// the job to every season and episode overview.
	TargetItem    TargetKind = "item"
	TargetSeason  TargetKind = "season"
	TargetEpisode TargetKind = "episode"
)

// JobStatus is the lifecycle state of a job, shared with the other AI job
// services via jobrunner.
type JobStatus = jobrunner.Status

// Job is a persisted metadata translation job. It is serialized to the API
// as-is.
type Job struct {
	ID              int64      `json:"id"`
	TargetKind      TargetKind `json:"target_kind"`
	ContentID       string     `json:"content_id"`
	IncludeChildren bool       `json:"include_children"`
	SourceLanguage  string     `json:"source_language"`
	TargetLanguage  string     `json:"target_language"`
	Engine          string     `json:"engine"`
	Model           string     `json:"model"`
	Status          JobStatus  `json:"status"`
	Progress        float64    `json:"progress"`
	ProgressMessage string     `json:"progress_message"`
	FieldsDone      int        `json:"fields_done"`
	FieldsTotal     int        `json:"fields_total"`
	Force           bool       `json:"force"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	IdempotencyKey  string     `json:"-"`
	RequestedBy     *int       `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	HeartbeatAt     time.Time  `json:"-"`
}

// JobRequest is the input to Service.Enqueue.
type JobRequest struct {
	TargetKind      TargetKind
	ContentID       string
	TargetLanguage  string
	IncludeChildren bool
	// Force re-translates fields that already have a provider or AI value.
	// Manual values are never overwritten regardless.
	Force       bool
	RequestedBy *int
}

// JobRepository persists metadata translation jobs.
type JobRepository interface {
	InsertJob(ctx context.Context, job *Job) error
	GetJob(ctx context.Context, id int64) (*Job, error)
	// GetActiveJobByIdempotencyKey returns a pending/running job with the given
	// key, or nil if none exists.
	GetActiveJobByIdempotencyKey(ctx context.Context, key string) (*Job, error)
	ListJobsByContent(ctx context.Context, contentID string) ([]Job, error)
	UpdateProgress(ctx context.Context, id int64, status JobStatus, progress float64, message string, fieldsDone, fieldsTotal int) error
	CompleteJob(ctx context.Context, id int64, message string, fieldsDone, fieldsTotal int) error
	FailJob(ctx context.Context, id int64, status JobStatus, message string) error
	Heartbeat(ctx context.Context, id int64) error
	ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error)
}

// ItemText is the translatable base-row text of a movie or series.
type ItemText struct {
	ContentID       string
	Type            string
	Title           string
	Year            int
	Overview        string
	Tagline         string
	DefaultLanguage string
}

// ChildText is the translatable base-row text of a season or episode.
// EpisodeNumber is zero for seasons.
type ChildText struct {
	ContentID     string
	SeasonNumber  int
	EpisodeNumber int
	Overview      string
}

// ContentReader loads the base-row text a job translates. Implementations
// return nil (not an error) for missing rows.
type ContentReader interface {
	ItemText(ctx context.Context, contentID string) (*ItemText, error)
	SeasonTexts(ctx context.Context, seriesID string) ([]ChildText, error)
	EpisodeTexts(ctx context.Context, seriesID string) ([]ChildText, error)
	// SeasonByID / EpisodeByID also return the parent series content ID.
	SeasonByID(ctx context.Context, contentID string) (*ChildText, string, error)
	EpisodeByID(ctx context.Context, contentID string) (*ChildText, string, error)
	// CountMissingFields counts base fields (item overview/tagline, season and
	// episode overviews) that have source text but no localized value for the
	// language — the auto-translate trigger condition.
	CountMissingFields(ctx context.Context, itemContentID, language string) (int, error)
}

// idempotencyKey derives the dedup key for a job. Two requests for the same
// target, language, and model collapse to one in-flight job.
func idempotencyKey(kind TargetKind, contentID, targetLang, model string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s", kind, contentID, targetLang, model))
	return hex.EncodeToString(sum[:])
}
