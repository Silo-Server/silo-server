package intromarkers

import (
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
)

func TestCreditsKeysAreNamespaced(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	key := creditsFingerprintKey()
	if key.Kind != ArtifactKindCreditsFingerprint || key.ConfigHash == cfg.ConfigHash() {
		t.Fatalf("credits key %+v must not share the intro fingerprint hash", key)
	}
	if key.ConfigHash != mediaartifact.ConfigHash(ArtifactKindCreditsFingerprint, creditsFingerprintParams) {
		t.Fatalf("credits key %+v is not derived from its kind and parameters", key)
	}
	if CreditsAnalysisConfigHash(true) == cfg.AnalysisConfigHash() || CreditsAnalysisConfigHash(false) == cfg.AnalysisConfigHash() {
		t.Fatal("credits and intro season state must use different hashes")
	}
}

// creditsSeason builds episodes whose tail fingerprints share sharedSeconds of
// audio that ends gapSeconds before the end of each file. Durations differ,
// so each file's tail window starts somewhere else.
func creditsSeason(episodes int, sharedSeconds, gapSeconds float64) []fingerprintInput {
	shared := randomPoints(int(sharedSeconds/DefaultPointHopSeconds), 7, 7)
	inputs := make([]fingerprintInput, 0, episodes)
	for e := 1; e <= episodes; e++ {
		candidate := Candidate{
			FileID: e, EpisodeID: string(rune('a' + e)), EpisodeNumber: e,
			FileHash: "h", FileSize: int64(e), DurationSeconds: 1500 + float64(e)*7,
		}
		window := tailWindow(candidate)
		points := randomPoints(int(window.duration()/DefaultPointHopSeconds), uint64(e), 3)
		end := len(points) - int(gapSeconds/DefaultPointHopSeconds)
		copy(points[end-len(shared):end], shared)
		inputs = append(inputs, fingerprintInput{Candidate: candidate, Points: points, WindowStart: window.Start})
	}
	return inputs
}

// placeAudioCredits compares a season's tail fingerprints and places each
// file's credits from its audio match alone, as a group without video does.
func placeAudioCredits(inputs []fingerprintInput) (map[int]Segment, int) {
	profile := creditsProfile()
	segments := map[int]Segment{}
	rejected := 0
	matches := matchSeason(inputs, profile)
	for _, input := range inputs {
		match, ok := matches[input.Candidate.FileID]
		if !ok {
			continue
		}
		audio := creditsAudioFor(match, profile)
		segment, ok := combineCredits(input.Candidate, creditsEvidence{Audio: &audio}, creditsLimitsFor(false))
		if !ok {
			rejected++
			continue
		}
		segments[input.Candidate.FileID] = segment
	}
	return segments, rejected
}

func TestAudioCreditsFindSharedEnding(t *testing.T) {
	inputs := creditsSeason(4, 60, 0)
	segments, rejected := placeAudioCredits(inputs)
	if len(segments) != 4 || rejected != 0 {
		t.Fatalf("placed %d files with %d rejected, want 4 and 0", len(segments), rejected)
	}
	for _, input := range inputs {
		segment := segments[input.Candidate.FileID]
		duration := input.Candidate.DurationSeconds
		// The shared audio covers the last 60 s; the Chromaprint leads land
		// the start within a couple of seconds of it.
		if math.Abs(segment.Start-(duration-60)) > 3 || segment.End != duration {
			t.Fatalf("file %d: credits %.1f-%.1f, want about %.1f to the %.1f s end", input.Candidate.FileID, segment.Start, segment.End, duration-60, duration)
		}
		if segment.Algorithm != CreditsAudioAlgorithm || segment.Confidence != creditsAudioConsistentConfidence {
			t.Fatalf("file %d: %+v, want %s at %.2f", input.Candidate.FileID, segment, CreditsAudioAlgorithm, creditsAudioConsistentConfidence)
		}
	}
}

func TestApplyCreditsGuards(t *testing.T) {
	limits := creditsLimitsFor(false)
	base := Candidate{DurationSeconds: 1500}
	withIntro := base
	withIntro.IntroStart, withIntro.IntroEnd = floatPtr(1000), floatPtr(1100)
	withPreview := base
	withPreview.PreviewStart = floatPtr(1470)
	lateIntro := base
	lateIntro.IntroStart, lateIntro.IntroEnd = floatPtr(0), floatPtr(60)
	segment := func(start, end, confidence float64) Segment {
		return Segment{Start: start, End: end, Confidence: confidence, Algorithm: CreditsAudioAlgorithm}
	}
	cases := []struct {
		name      string
		candidate Candidate
		segment   Segment
		wantOK    bool
		wantEnd   float64
	}{
		{"inside the tail", base, segment(1400, 1500, 0.9), true, 1500},
		{"starts before the tail window", base, segment(1000, 1440, 0.9), false, 0},
		{"too short", base, segment(1490, 1500, 0.9), false, 0},
		{"before the intro ends", withIntro, segment(1060, 1500, 0.9), false, 0},
		{"after an early intro", lateIntro, segment(1400, 1500, 0.9), true, 1500},
		{"a preview inside ends it", withPreview, segment(1400, 1500, 0.9), true, 1470},
		{"a preview that leaves too little", withPreview, segment(1460, 1500, 0.9), false, 0},
		{"past the end of the file", base, segment(1400, 1510, 0.9), false, 0},
		{"too little confidence", base, segment(1400, 1500, 0.5), false, 0},
	}
	for _, tc := range cases {
		got, ok := applyCreditsGuards(tc.segment, tc.candidate, limits)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if ok && (got.End != tc.wantEnd || got.Start != tc.segment.Start) {
			t.Errorf("%s: %.0f-%.0f, want %.0f-%.0f", tc.name, got.Start, got.End, tc.segment.Start, tc.wantEnd)
		}
	}
}

func TestCopyCreditsToVersion(t *testing.T) {
	source := chapterSourceMarker{
		candidate: Candidate{FileID: 1, EpisodeID: "e1", DurationSeconds: 1500},
		segment:   Segment{Start: 1410, End: 1500, Confidence: 0.95, Algorithm: CreditsChapterAlgorithm},
	}
	cases := []struct {
		name               string
		duration           float64
		sourceEnd          float64
		wantOK             bool
		wantStart, wantEnd float64
	}{
		{"shorter version keeps its distance from the end", 1497.5, 1500, true, 1407.5, 1497.5},
		{"longer version", 1503, 1500, true, 1413, 1503},
		{"credits that stop early stay end-anchored", 1502, 1470, true, 1412, 1472},
		{"more than three seconds apart", 1504, 1500, false, 0, 0},
	}
	for _, tc := range cases {
		src := source
		src.segment.End = tc.sourceEnd
		got, ok := copyCreditsToVersion(src, Candidate{FileID: 2, EpisodeID: "e1", DurationSeconds: tc.duration})
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(got.Start-tc.wantStart) > 1e-9 || math.Abs(got.End-tc.wantEnd) > 1e-9 {
			t.Errorf("%s: %.1f-%.1f, want %.1f-%.1f", tc.name, got.Start, got.End, tc.wantStart, tc.wantEnd)
		}
		if got.Algorithm != CreditsVersionCopyAlgorithm || got.Confidence != 0.85 {
			t.Errorf("%s: %+v, want %s at 0.85", tc.name, got, CreditsVersionCopyAlgorithm)
		}
	}
}
