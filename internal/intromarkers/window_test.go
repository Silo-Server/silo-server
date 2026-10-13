package intromarkers

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestHeadWindowIsTheIntroAnalysisWindow(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	candidate := Candidate{FileHash: "h", FileSize: 10, DurationSeconds: 1500}
	window := headWindow(candidate, cfg)
	if window.Start != 0 || window.End != 375 {
		t.Fatalf("head window = %+v, want 0-375 (25%% of 1500 s)", window)
	}
	if got := window.identity(candidate); got != introFingerprintIdentity(candidate, cfg) || got.WindowEndSeconds != 375 {
		t.Fatalf("window identity = %+v, want the intro fingerprint identity", got)
	}
	if !headWindow(Candidate{}, cfg).empty() {
		t.Fatal("a file without a duration has an empty window")
	}
}

func TestAdjustSegmentAddsTheWindowStart(t *testing.T) {
	profile := introProfile(DefaultConfig("ffmpeg"))
	candidate := Candidate{DurationSeconds: 1500}
	got := adjustSegment(Segment{Start: 0.4, End: 60}, fingerprintInput{Candidate: candidate, WindowStart: 1050}, profile)
	// The start snap applies to the start of the file, not of the window.
	if want := 1050 + 0.4 + chromaprintStartLeadSeconds; math.Abs(got.Start-want) > 1e-9 {
		t.Fatalf("start = %.3f, want %.3f", got.Start, want)
	}
	if want := 1050 + 60 + chromaprintEndLeadSeconds; math.Abs(got.End-want) > 1e-9 {
		t.Fatalf("end = %.3f, want %.3f", got.End, want)
	}

	// An end past the file is clamped to its duration.
	got = adjustSegment(Segment{Start: 400, End: 460}, fingerprintInput{Candidate: candidate, WindowStart: 1050}, profile)
	if got.End != 1500 {
		t.Fatalf("end = %.3f, want the 1500 s file end", got.End)
	}
}

// windowedInputs builds four episodes whose fingerprints share a 300-point
// segment at point 100, each fingerprinted from windowStart.
func windowedInputs(windowStart float64) []fingerprintInput {
	shared := make([]uint32, 300)
	sharedRNG := rand.New(rand.NewPCG(0, 1))
	for i := range shared {
		shared[i] = sharedRNG.Uint32()
	}
	inputs := make([]fingerprintInput, 0, 4)
	for e := 1; e <= 4; e++ {
		points := make([]uint32, 600)
		rng := rand.New(rand.NewPCG(uint64(e), 9))
		for i := range points {
			points[i] = rng.Uint32()
		}
		copy(points[100:], shared)
		inputs = append(inputs, fingerprintInput{
			Candidate:   Candidate{FileID: e, EpisodeID: string(rune('a' + e)), EpisodeNumber: e, DurationSeconds: 3000},
			Points:      points,
			WindowStart: windowStart,
		})
	}
	return inputs
}

func TestCompareFingerprintsShiftsSegmentsByTheWindowStart(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	head := CompareFingerprints(windowedInputs(0), cfg)
	tail := CompareFingerprints(windowedInputs(2000), cfg)
	if len(head) != 4 || len(tail) != 4 {
		t.Fatalf("matched %d head and %d tail files, want 4 each", len(head), len(tail))
	}
	for fileID, segment := range head {
		shifted := tail[fileID]
		if math.Abs(shifted.Start-(segment.Start+2000)) > 1e-9 || math.Abs(shifted.End-(segment.End+2000)) > 1e-9 {
			t.Fatalf("file %d: tail window segment %+v, want the head segment %+v moved by 2000 s", fileID, shifted, segment)
		}
		if shifted.Confidence != segment.Confidence || shifted.Algorithm != segment.Algorithm {
			t.Fatalf("file %d: window start changed the rating: %+v vs %+v", fileID, shifted, segment)
		}
	}
}

func TestIntroProfileKeepsIntroBounds(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	cfg.MinimumIntroDurationSeconds, cfg.MaximumIntroDurationSeconds = 8, 200
	profile := introProfile(cfg)
	if profile.MinSeconds != 8 || profile.MaxSeconds != 200 {
		t.Fatalf("pair bounds = %.0f-%.0f, want the configured 8-200", profile.MinSeconds, profile.MaxSeconds)
	}
	if profile.AdjustedMinSeconds != 10 || profile.AdjustedMaxSeconds != 180 {
		t.Fatalf("adjusted bounds = %.0f-%.0f, want 10-180", profile.AdjustedMinSeconds, profile.AdjustedMaxSeconds)
	}
	if profile.Algorithm != ChromaprintAlgorithm || profile.ZeroStartSnapSeconds != zeroStartSnapSeconds {
		t.Fatalf("profile = %+v", profile)
	}
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
