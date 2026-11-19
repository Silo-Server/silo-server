package intromarkers

import (
	"testing"
)

// windowedInputs builds four episodes whose fingerprints share a 300-point
// segment at point 100, each fingerprinted from windowStart.
func windowedInputs(windowStart float64) []fingerprintInput {
	shared := randomPoints(300, 0, 1)
	inputs := make([]fingerprintInput, 0, 4)
	for e := 1; e <= 4; e++ {
		points := randomPoints(600, uint64(e), 9)
		copy(points[100:], shared)
		inputs = append(inputs, fingerprintInput{
			Candidate:   Candidate{FileID: e, EpisodeID: string(rune('a' + e)), EpisodeNumber: e, DurationSeconds: 3000},
			Points:      points,
			WindowStart: windowStart,
		})
	}
	return inputs
}

func TestCompareFingerprintsAppliesProfileBounds(t *testing.T) {
	// The shared segment runs 300 points, about 37 s; chance matches in the
	// random audio around it stretch that by a few seconds.
	inputs := windowedInputs(0)
	profile := introProfile(DefaultConfig("ffmpeg"))
	if got := compareFingerprints(inputs, profile); len(got) != 4 {
		t.Fatalf("matched %d files, want 4", len(got))
	}

	tooShort := profile
	tooShort.MinSeconds = 60
	if got := compareFingerprints(inputs, tooShort); len(got) != 0 {
		t.Fatalf("pair minimum above the match kept %d files", len(got))
	}
	adjustedTooLong := profile
	adjustedTooLong.AdjustedMaxSeconds = 30
	if got := compareFingerprints(inputs, adjustedTooLong); len(got) != 0 {
		t.Fatalf("adjusted maximum below the match kept %d files", len(got))
	}

	credits := profile
	credits.Algorithm = "credits-test:v1"
	credits.InconsistentConfidence, credits.ConsistentConfidence = 0.5, 0.8
	creditsGot := compareFingerprints(inputs, credits)
	if len(creditsGot) != 4 {
		t.Fatalf("custom profile matched %d files, want 4", len(creditsGot))
	}
	for fileID, segment := range creditsGot {
		if segment.Algorithm != "credits-test:v1" || segment.Confidence != 0.8 {
			t.Fatalf("file %d: segment %+v, want the profile's algorithm and consistent confidence", fileID, segment)
		}
	}
}
