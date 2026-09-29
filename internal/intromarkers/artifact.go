package intromarkers

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Artifact kinds stored in media_intro_fingerprints, which holds every
// per-file media analysis result; the table name predates generalization.
const (
	// ArtifactKindIntroFingerprint is the raw Chromaprint of a file's
	// opening audio. Its config_hash is Config.ConfigHash, which predates
	// kind namespacing and must not change.
	ArtifactKindIntroFingerprint = "intro_fingerprint"
)

// Artifact row statuses.
const (
	// ArtifactComplete carries a payload that stands while the file and
	// window it was computed from are unchanged.
	ArtifactComplete = "complete"
	// ArtifactUnusable records that the file cannot yield this artifact, for
	// the reason in Detail. It stands until the file or the config changes.
	ArtifactUnusable = "unusable"
	// ArtifactFailed records an error that may be transient. Only the server
	// that recorded it waits for RetryAfter; others retry at once, since the
	// cause may be local to that server.
	ArtifactFailed = "failed"
)

const (
	retryBaseDelay = 12 * time.Hour
	retryMaxDelay  = 7 * 24 * time.Hour
)

// ArtifactConfigHash derives a kind's config_hash from the parameters that
// shape its payload. The kind prefix keeps kinds from sharing a primary key,
// which is (media_file_id, algorithm_version, config_hash). Intro
// fingerprints keep Config.ConfigHash instead.
func ArtifactConfigHash(kind, params string) string {
	sum := sha256.Sum256([]byte(kind + ":" + params))
	return hex.EncodeToString(sum[:])[:16]
}

// ArtifactKey selects one artifact of a file.
type ArtifactKey struct {
	Kind             string
	AlgorithmVersion int
	ConfigHash       string
}

// ArtifactIdentity is the file and window an artifact was computed from. A
// stored row applies only while every field still matches.
type ArtifactIdentity struct {
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
	ArtifactKey
	ArtifactIdentity
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

// ArtifactFailure is an analysis error to record for a file.
type ArtifactFailure struct {
	MediaFileID int
	ArtifactKey
	ArtifactIdentity
	RecordedBy string
	Error      string
	At         time.Time
}

// ArtifactState is what a stored artifact means for a file now.
type ArtifactState int

const (
	// ArtifactMissing means the artifact has to be computed.
	ArtifactMissing ArtifactState = iota
	// ArtifactReady means the stored payload can be used.
	ArtifactReady
	// ArtifactSkipped means the file should not be analyzed now: it is
	// unusable, or this server's last attempt failed and is backing off.
	ArtifactSkipped
)

// State reports what a (possibly nil) stored artifact means for a file with
// the given identity, on server node, at now.
func (a *Artifact) State(identity ArtifactIdentity, node string, now time.Time) ArtifactState {
	if a == nil || a.ArtifactIdentity != identity {
		return ArtifactMissing
	}
	switch a.Status {
	case ArtifactComplete:
		return ArtifactReady
	case ArtifactUnusable:
		return ArtifactSkipped
	case ArtifactFailed:
		if a.RecordedBy == node && a.RetryAfter != nil && now.Before(*a.RetryAfter) {
			return ArtifactSkipped
		}
	}
	return ArtifactMissing
}

// nextArtifactFailure returns the failure count and retry time to record for
// failure, given the row stored before it. Backoff escalates only over one
// server's consecutive failures on unchanged inputs, and a failure inside the
// current backoff window (a forced analysis) does not escalate it.
func nextArtifactFailure(previous *Artifact, failure ArtifactFailure) (int, time.Time) {
	if previous == nil || previous.Status != ArtifactFailed ||
		previous.RecordedBy != failure.RecordedBy ||
		previous.ArtifactIdentity != failure.ArtifactIdentity {
		return 1, failure.At.Add(retryDelay(1))
	}
	if previous.RetryAfter != nil && failure.At.Before(*previous.RetryAfter) {
		return max(previous.FailureCount, 1), *previous.RetryAfter
	}
	count := previous.FailureCount + 1
	return count, failure.At.Add(retryDelay(count))
}

// retryDelay is the backoff after a number of consecutive failures, for
// artifacts and chapter silence refinements alike. It doubles from
// retryBaseDelay per failure, capped at retryMaxDelay. The base sits under the
// daily schedule so the first retry lands on the next scheduled run.
func retryDelay(failures int) time.Duration {
	delay := retryBaseDelay
	for i := 1; i < failures && delay < retryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, retryMaxDelay)
}
