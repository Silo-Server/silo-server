package intromarkers

import (
	"math"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
)

// fingerprintWindow is the stretch of a file, in media seconds, whose audio a
// marker kind fingerprints. Fingerprint points count from Start, so matches
// found in the window are shifted by Start into file time.
type fingerprintWindow struct {
	Start float64
	End   float64
}

// headWindow is the opening of the file intro detection fingerprints.
func headWindow(candidate Candidate, cfg Config) fingerprintWindow {
	return fingerprintWindow{Start: 0, End: analysisWindowEnd(candidate.DurationSeconds, cfg)}
}

func (w fingerprintWindow) duration() float64 {
	return w.End - w.Start
}

func (w fingerprintWindow) empty() bool {
	return w.End <= w.Start
}

// identity is the artifact identity of a fingerprint of this window of the
// candidate's file.
func (w fingerprintWindow) identity(candidate Candidate) mediaartifact.Identity {
	return mediaartifact.Identity{
		FileHash:           candidate.FileHash,
		FileSize:           candidate.FileSize,
		DurationSeconds:    candidate.DurationSeconds,
		WindowStartSeconds: w.Start,
		WindowEndSeconds:   w.End,
	}
}

func analysisWindowEnd(duration float64, cfg Config) float64 {
	if duration <= 0 {
		return 0
	}
	percentEnd := duration * (float64(cfg.AnalysisPercent) / 100)
	limitEnd := float64(cfg.AnalysisLengthLimitMinutes * 60)
	return math.Min(duration, math.Min(percentEnd, limitEnd))
}
