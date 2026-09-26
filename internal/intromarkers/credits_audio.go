package intromarkers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Credits audio matching. A season's end credits usually share their music,
// so tail fingerprints are compared across the season like intros. The
// validation found real credits confirmed by two or more partner episodes;
// a single confirmation, or a short match most of the season does not share,
// was often a recurring music cue instead.
const (
	creditsAudioConsistentConfidence   = 0.90
	creditsAudioInconsistentConfidence = 0.65
	// creditsShortSeconds is the duration below which a match must be
	// season-consistent to count.
	creditsShortSeconds = 20.0
	// creditsSeasonToleranceSeconds is how far a file's credits duration may
	// be from the season's usual duration and still agree with it. Credits
	// vary more than intros because they end at the end of the file.
	creditsSeasonToleranceSeconds = 3.0
	// creditsMinimumConfirmations is how many partner episodes must agree
	// with a file's match before audio alone may place its credits.
	creditsMinimumConfirmations = 2
)

// creditsProfile is the credits comparison over episode tail windows. An end
// near the end of the file snaps to it; starts do not snap, since credits
// never start at the start of the file, and neither boundary snaps to
// chapters, which the validated rules did not do.
func creditsProfile() matchProfile {
	limits := creditsLimitsFor(false)
	return matchProfile{
		MinSeconds:             limits.minSeconds,
		MaxSeconds:             limits.maxSeconds,
		AdjustedMinSeconds:     limits.minSeconds,
		AdjustedMaxSeconds:     limits.maxSeconds,
		ShortSeconds:           creditsShortSeconds,
		SeasonToleranceSeconds: creditsSeasonToleranceSeconds,
		Algorithm:              CreditsAudioAlgorithm,
		ConsistentConfidence:   creditsAudioConsistentConfidence,
		InconsistentConfidence: creditsAudioInconsistentConfidence,
		EOFSnapSeconds:         creditsEOFSnapSeconds,
	}
}

// compareCreditsFingerprints matches the season's tail fingerprints and
// returns the credits audio alone can place, by file ID.
func compareCreditsFingerprints(inputs []fingerprintInput) (map[int]Segment, int) {
	durations := make(map[int]float64, len(inputs))
	for _, input := range inputs {
		durations[input.Candidate.FileID] = input.Candidate.DurationSeconds
	}
	profile := creditsProfile()
	segments := map[int]Segment{}
	rejected := 0
	for fileID, match := range matchSeason(inputs, profile) {
		segment, ok := rateCreditsMatch(match, durations[fileID], profile)
		if !ok {
			rejected++
			continue
		}
		segments[fileID] = segment
	}
	return segments, rejected
}

// rateCreditsMatch rates a file's credits audio match, or rejects it. Only a
// strong match counts: confirmed by at least two partner episodes, at least
// creditsShortSeconds long unless the season agrees on its duration, and
// running to the end of the file. Without video to corroborate it, a match
// that stops early is most likely a recurring cue.
func rateCreditsMatch(match seasonMatch, duration float64, profile matchProfile) (Segment, bool) {
	segment := match.Segment
	if match.Confirmations < creditsMinimumConfirmations {
		return Segment{}, false
	}
	if segment.End-segment.Start < profile.ShortSeconds && !match.SeasonConsistent {
		return Segment{}, false
	}
	if !reachesEOF(segment.End, duration) {
		return Segment{}, false
	}
	segment.Confidence = profile.InconsistentConfidence
	if match.SeasonConsistent {
		segment.Confidence = profile.ConsistentConfidence
	}
	segment.Algorithm = profile.Algorithm
	return segment, true
}

// ExtractCredits fingerprints the candidate's tail audio for credits
// detection.
func (e *ChromaprintExtractor) ExtractCredits(ctx context.Context, candidate Candidate) (Fingerprint, bool, error) {
	window := tailWindow(candidate)
	points, err := e.extractWindow(ctx, candidate, window)
	if err != nil || len(points) == 0 {
		return Fingerprint{}, false, err
	}
	key := creditsFingerprintKey()
	return Fingerprint{
		MediaFileID:           candidate.FileID,
		FileHash:              candidate.FileHash,
		FileSize:              candidate.FileSize,
		DurationSeconds:       candidate.DurationSeconds,
		WindowStartSeconds:    window.Start,
		WindowEndSeconds:      window.End,
		AlgorithmVersion:      key.AlgorithmVersion,
		ConfigHash:            key.ConfigHash,
		FingerprintFormat:     ChromaprintFormat,
		SampleDurationSeconds: float64(len(points)) * DefaultPointHopSeconds,
		Points:                points,
	}, true, nil
}

// creditsFingerprintDetailNoAudio marks a tail window with no audio to
// fingerprint.
const creditsFingerprintDetailNoAudio = "no_audio"

// loadCreditsArtifacts reads the stored credits fingerprints of candidates
// in one query within the lookup bound.
func (a *Analyzer) loadCreditsArtifacts(ctx context.Context, candidates []Candidate) (map[int]Artifact, error) {
	fileIDs := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		fileIDs = append(fileIDs, candidate.FileID)
	}
	var artifacts map[int]Artifact
	err := a.withLookupSlot(ctx, func() error {
		var err error
		artifacts, err = a.repo.LoadArtifacts(ctx, fileIDs, creditsFingerprintKey())
		return err
	})
	return artifacts, err
}

// creditsFingerprint interprets a candidate's stored credits artifact: the
// cached fingerprint, or why there is none.
func (a *Analyzer) creditsFingerprint(artifact *Artifact, candidate Candidate) (*Fingerprint, fingerprintLookup) {
	switch artifact.State(tailWindow(candidate).identity(candidate), a.nodeName(), time.Now()) {
	case ArtifactReady:
	case ArtifactSkipped:
		if artifact.Status == ArtifactFailed {
			return nil, fingerprintDeferred
		}
		return nil, fingerprintUnusable
	default:
		return nil, fingerprintMissing
	}
	points := mediasample.DecodeRawFingerprint(artifact.Payload)
	if artifact.PayloadFormat != ChromaprintFormat || len(points) == 0 {
		return nil, fingerprintMissing
	}
	return &Fingerprint{
		MediaFileID:           artifact.MediaFileID,
		FileHash:              artifact.FileHash,
		FileSize:              artifact.FileSize,
		DurationSeconds:       artifact.DurationSeconds,
		WindowStartSeconds:    artifact.WindowStartSeconds,
		WindowEndSeconds:      artifact.WindowEndSeconds,
		AlgorithmVersion:      artifact.AlgorithmVersion,
		ConfigHash:            artifact.ConfigHash,
		FingerprintFormat:     artifact.PayloadFormat,
		SampleDurationSeconds: artifact.SampleDurationSeconds,
		Points:                points,
	}, fingerprintCached
}

// storeCreditsFingerprint caches a computed credits fingerprint.
func (a *Analyzer) storeCreditsFingerprint(ctx context.Context, fp Fingerprint) error {
	return a.repo.UpsertArtifact(ctx, Artifact{
		MediaFileID: fp.MediaFileID,
		ArtifactKey: creditsFingerprintKey(),
		ArtifactIdentity: ArtifactIdentity{
			FileHash:           fp.FileHash,
			FileSize:           fp.FileSize,
			DurationSeconds:    fp.DurationSeconds,
			WindowStartSeconds: fp.WindowStartSeconds,
			WindowEndSeconds:   fp.WindowEndSeconds,
		},
		Status:                ArtifactComplete,
		PayloadFormat:         fp.FingerprintFormat,
		SampleDurationSeconds: fp.SampleDurationSeconds,
		ItemCount:             len(fp.Points),
		Payload:               mediasample.EncodeRawFingerprint(fp.Points),
	})
}

// storeCreditsNoAudio records that the candidate's tail has no audio to
// fingerprint, so it is not decoded again until the file changes.
func (a *Analyzer) storeCreditsNoAudio(ctx context.Context, candidate Candidate) error {
	return a.repo.UpsertArtifact(ctx, Artifact{
		MediaFileID:      candidate.FileID,
		ArtifactKey:      creditsFingerprintKey(),
		ArtifactIdentity: tailWindow(candidate).identity(candidate),
		Status:           ArtifactUnusable,
		Detail:           creditsFingerprintDetailNoAudio,
	})
}

// recordCreditsFingerprintFailure records a failed tail extraction so this
// server backs off before decoding the file again.
func (a *Analyzer) recordCreditsFingerprintFailure(ctx context.Context, candidate Candidate, extractErr error) error {
	return a.repo.RecordArtifactFailure(ctx, ArtifactFailure{
		MediaFileID:      candidate.FileID,
		ArtifactKey:      creditsFingerprintKey(),
		ArtifactIdentity: tailWindow(candidate).identity(candidate),
		RecordedBy:       a.nodeName(),
		Error:            extractErr.Error(),
		At:               time.Now().UTC(),
	})
}
