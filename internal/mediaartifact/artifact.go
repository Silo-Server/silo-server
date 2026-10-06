// Package mediaartifact stores per-file media analysis results: one row per
// file and artifact kind, with a status that says whether the result can be
// used, should be skipped, or has to be computed again. Each kind owns the
// encoding of its payload; this package only stores the bytes.
package mediaartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Artifact row statuses.
const (
	// StatusComplete carries a payload that stands while the file and
	// window it was computed from are unchanged.
	StatusComplete = "complete"
	// StatusUnusable records that the file cannot yield this artifact, for
	// the reason in Detail. It stands until the file or the config changes.
	StatusUnusable = "unusable"
	// StatusFailed records an error that may be transient. Only the server
	// that recorded it waits for RetryAfter; others retry at once, since the
	// cause may be local to that server.
	StatusFailed = "failed"
)

const (
	retryBaseDelay = 12 * time.Hour
	retryMaxDelay  = 7 * 24 * time.Hour
)

// ConfigHash derives a kind's config_hash from the parameters that shape its
// payload. The kind prefix keeps kinds from sharing a primary key, which is
// (media_file_id, algorithm_version, config_hash). Intro fingerprints keep
// intromarkers.Config.ConfigHash instead.
func ConfigHash(kind, params string) string {
	sum := sha256.Sum256([]byte(kind + ":" + params))
	return hex.EncodeToString(sum[:])[:16]
}

// Key selects one artifact of a file.
type Key struct {
	Kind             string
	AlgorithmVersion int
	ConfigHash       string
}

// Identity is the file and window an artifact was computed from. A stored
// row applies only while every field still matches.
type Identity struct {
	FileHash           string
	FileSize           int64
	DurationSeconds    float64
	WindowStartSeconds float64
	WindowEndSeconds   float64
}

// Artifact is one row of media_intro_fingerprints. Payload is stored in the
// points column and ItemCount in point_count; their encoding belongs to the
// kind. PayloadFormat (fingerprint_format) names that encoding and
// SampleDurationSeconds (sample_duration_seconds) is the media time the
// payload covers, or zero when a kind has no use for it.
type Artifact struct {
	MediaFileID int
	Key
	Identity
	Status                string
	Detail                string
	PayloadFormat         string
	SampleDurationSeconds float64
	ItemCount             int
	Payload               []byte
	FailureCount          int
	LastError             string
	RetryAfter            *time.Time
	RecordedBy            string
	UpdatedAt             time.Time
}

// Failure is an analysis error to record for a file.
type Failure struct {
	MediaFileID int
	Key
	Identity
	RecordedBy string
	Error      string
	At         time.Time
}

// State is what a stored artifact means for a file now.
type State int

const (
	// Missing means the artifact has to be computed.
	Missing State = iota
	// Ready means the stored payload can be used.
	Ready
	// Skipped means the file should not be analyzed now: it is unusable, or
	// this server's last attempt failed and is backing off.
	Skipped
)

// State reports what a (possibly nil) stored artifact means for a file with
// the given identity, on server node, at now.
func (a *Artifact) State(identity Identity, node string, now time.Time) State {
	if a == nil || a.Identity != identity {
		return Missing
	}
	switch a.Status {
	case StatusComplete:
		return Ready
	case StatusUnusable:
		return Skipped
	case StatusFailed:
		if a.RecordedBy == node && a.RetryAfter != nil && now.Before(*a.RetryAfter) {
			return Skipped
		}
	}
	return Missing
}

// NextFailure returns the failure count and retry time to record for
// failure, given the row stored before it. Backoff escalates only over one
// server's consecutive failures on unchanged inputs, and a failure inside the
// current backoff window (a forced analysis) does not escalate it.
func NextFailure(previous *Artifact, failure Failure) (int, time.Time) {
	if previous == nil || previous.Status != StatusFailed ||
		previous.RecordedBy != failure.RecordedBy ||
		previous.Identity != failure.Identity {
		return 1, failure.At.Add(RetryDelay(1))
	}
	if previous.RetryAfter != nil && failure.At.Before(*previous.RetryAfter) {
		return max(previous.FailureCount, 1), *previous.RetryAfter
	}
	count := previous.FailureCount + 1
	return count, failure.At.Add(RetryDelay(count))
}

// RetryDelay is the backoff after a number of consecutive failures, for
// artifacts and other per-file analysis attempts alike (intro detection's
// chapter silence refinements use it too). It doubles from retryBaseDelay per
// failure, capped at retryMaxDelay. The base sits under the daily schedule so
// the first retry lands on the next scheduled run.
func RetryDelay(failures int) time.Duration {
	delay := retryBaseDelay
	for i := 1; i < failures && delay < retryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, retryMaxDelay)
}
