package intromarkers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Credits detection bounds, validated against frame-checked episodes and
// movies. Credits start in the file's tail window: the last TailSeconds, or
// TailFraction of a shorter file. An end this close to the end of the file is
// the end of the file.
const (
	creditsEOFSnapSeconds = 15.0
	creditsMinimumSeconds = 15.0

	episodeCreditsTailSeconds    = 450.0
	episodeCreditsTailFraction   = 0.4
	episodeCreditsMaximumSeconds = 450.0
	// episodeCreditsVideoEOFSeconds is how far before the end of an episode
	// credits placed by video alone may end. An earlier text run was a dark
	// letterboxed scene or a title card inside the story.
	episodeCreditsVideoEOFSeconds = 120.0

	movieCreditsTailSeconds     = 900.0
	movieCreditsTailFraction    = 0.25
	movieCreditsMaximumSeconds  = 900.0
	movieCreditsVideoEOFSeconds = 180.0
)

// Credits confidences. Chapter credits are authoritative; a copy to another
// version of the episode rates below the chapter it came from.
const (
	creditsChapterConfidence     = 0.95
	creditsVersionCopyConfidence = 0.85
	// creditsMinimumConfidence is the lowest confidence a credits marker is
	// written with.
	creditsMinimumConfidence = 0.55
)

// creditsLimits are the tail window and length bounds for one kind of file.
type creditsLimits struct {
	tailSeconds  float64
	tailFraction float64
	minSeconds   float64
	maxSeconds   float64
	// videoEOFSeconds bounds how far before the end of the file credits
	// placed by video alone may end.
	videoEOFSeconds float64
}

func creditsLimitsFor(isMovie bool) creditsLimits {
	if isMovie {
		return creditsLimits{
			tailSeconds:     movieCreditsTailSeconds,
			tailFraction:    movieCreditsTailFraction,
			minSeconds:      creditsMinimumSeconds,
			maxSeconds:      movieCreditsMaximumSeconds,
			videoEOFSeconds: movieCreditsVideoEOFSeconds,
		}
	}
	return creditsLimits{
		tailSeconds:     episodeCreditsTailSeconds,
		tailFraction:    episodeCreditsTailFraction,
		minSeconds:      creditsMinimumSeconds,
		maxSeconds:      episodeCreditsMaximumSeconds,
		videoEOFSeconds: episodeCreditsVideoEOFSeconds,
	}
}

// windowStart is where the tail window of a file of duration seconds begins.
func (l creditsLimits) windowStart(duration float64) float64 {
	if duration <= 0 {
		return 0
	}
	return duration - math.Min(l.tailSeconds, l.tailFraction*duration)
}

// tailWindow is the end of an episode that credits detection fingerprints.
func tailWindow(candidate Candidate) fingerprintWindow {
	duration := candidate.DurationSeconds
	if duration <= 0 {
		return fingerprintWindow{}
	}
	return fingerprintWindow{Start: creditsLimitsFor(false).windowStart(duration), End: duration}
}

// snapCreditsEnd moves an end within the EOF snap to the end of the file.
func snapCreditsEnd(end, duration float64) float64 {
	if duration > 0 && duration-end <= creditsEOFSnapSeconds {
		return duration
	}
	return end
}

// reachesEOF reports whether credits ending at end run to the end of a file
// of duration seconds, within the EOF snap.
func reachesEOF(end, duration float64) bool {
	return duration > 0 && duration-end <= creditsEOFSnapSeconds
}

// ArtifactKindCreditsFingerprint is the raw Chromaprint of an episode's tail
// window.
const ArtifactKindCreditsFingerprint = "credits_fingerprint"

// creditsFingerprintParams are the parameters that shape a credits
// fingerprint. Changing them discards every cached credits fingerprint.
var creditsFingerprintParams = fmt.Sprintf("tail=%.0f:%.2f;format=%s",
	episodeCreditsTailSeconds, episodeCreditsTailFraction, ChromaprintFormat)

// creditsFingerprintKey keys an episode's cached credits fingerprint.
func creditsFingerprintKey() ArtifactKey {
	return ArtifactKey{
		Kind:             ArtifactKindCreditsFingerprint,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       ArtifactConfigHash(ArtifactKindCreditsFingerprint, creditsFingerprintParams),
	}
}

// CreditsAnalysisConfigHash keys credits season analysis state: the credits
// fingerprint key, the tail key when the analysis ran tail passes, and
// CreditsBehaviorVersion. A group settled without tail passes, while ffmpeg
// lacked what they need, is analyzed again once they can run. It never
// equals an intro analysis hash, so the two kinds keep separate season state.
func CreditsAnalysisConfigHash(creditsTail bool) string {
	tailHash := "none"
	if creditsTail {
		tailHash = creditsTailKey().ConfigHash
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("credits:%s:%s:%d",
		creditsFingerprintKey().ConfigHash,
		tailHash,
		CreditsBehaviorVersion,
	)))
	return hex.EncodeToString(sum[:])[:16]
}

// creditsInputSignature is InputSignature plus whether each file's probe
// metadata rules out its tail pass. A probe repair that fills in a missing
// or misread video codec changes it, so a settled group is analyzed again.
func creditsInputSignature(candidates []Candidate) string {
	parts := make([]string, 0, len(candidates)+1)
	for _, c := range candidates {
		parts = append(parts, fmt.Sprintf("%d:%s", c.FileID, tailUnusableBeforeSampling(c)))
	}
	sort.Strings(parts)
	parts = append(parts, InputSignature(candidates))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}
