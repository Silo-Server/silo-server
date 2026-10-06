package intromarkers

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestTailWindow(t *testing.T) {
	cases := []struct {
		duration, start float64
	}{
		// Long episodes fingerprint their last 450 seconds.
		{2700, 2250},
		{1500, 1050},
		// Shorter ones their last 40 percent.
		{900, 540},
		{300, 180},
	}
	for _, tc := range cases {
		candidate := Candidate{FileHash: "h", FileSize: 1, DurationSeconds: tc.duration}
		window := tailWindow(candidate)
		if window.Start != tc.start || window.End != tc.duration {
			t.Errorf("duration %.0f: tail window %+v, want %.0f-%.0f", tc.duration, window, tc.start, tc.duration)
		}
		identity := window.identity(candidate)
		if identity.WindowStartSeconds != tc.start || identity.WindowEndSeconds != tc.duration {
			t.Errorf("duration %.0f: identity %+v does not record the window", tc.duration, identity)
		}
	}
	if !tailWindow(Candidate{}).empty() {
		t.Fatal("a file without a duration has an empty tail window")
	}
	// Movies get a longer, proportionally smaller tail.
	if got := creditsLimitsFor(true).windowStart(7200); got != 6300 {
		t.Fatalf("movie window start = %.0f, want 6300", got)
	}
	if got := creditsLimitsFor(true).windowStart(2400); got != 1800 {
		t.Fatalf("short movie window start = %.0f, want 1800", got)
	}
}

func TestCreditsFingerprintRequestSamplesTheTail(t *testing.T) {
	candidate := Candidate{FilePath: "/media/e1.mkv", DurationSeconds: 1500}
	req := fingerprintRequest(context.Background(), candidate, tailWindow(candidate))
	if req.Window == nil || req.Window.StartSeconds != 1050 || req.Window.DurationSeconds != 450 {
		t.Fatalf("request window = %+v, want 1050 s for 450 s", req.Window)
	}
	if req.Audio == nil || !req.Audio.Fingerprint {
		t.Fatalf("request audio = %+v, want a fingerprint", req.Audio)
	}
}

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
	shared := make([]uint32, int(sharedSeconds/DefaultPointHopSeconds))
	sharedRNG := rand.New(rand.NewPCG(7, 7))
	for i := range shared {
		shared[i] = sharedRNG.Uint32()
	}
	inputs := make([]fingerprintInput, 0, episodes)
	for e := 1; e <= episodes; e++ {
		candidate := Candidate{
			FileID: e, EpisodeID: string(rune('a' + e)), EpisodeNumber: e,
			FileHash: "h", FileSize: int64(e), DurationSeconds: 1500 + float64(e)*7,
		}
		window := tailWindow(candidate)
		points := make([]uint32, int(window.duration()/DefaultPointHopSeconds))
		rng := rand.New(rand.NewPCG(uint64(e), 3))
		for i := range points {
			points[i] = rng.Uint32()
		}
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

func TestAudioCreditsWithoutVideoRequireEOF(t *testing.T) {
	// Shared audio that stops a minute before the end is more likely a
	// recurring cue than the credits.
	segments, rejected := placeAudioCredits(creditsSeason(4, 60, 60))
	if len(segments) != 0 || rejected != 4 {
		t.Fatalf("placed %d files with %d rejected, want none placed and 4 rejected", len(segments), rejected)
	}
}

func TestAudioCreditsWithoutVideoNeedTwoConfirmations(t *testing.T) {
	// Two episodes confirm each other once each: weak evidence, not written
	// without video.
	segments, rejected := placeAudioCredits(creditsSeason(2, 60, 0))
	if len(segments) != 0 || rejected != 2 {
		t.Fatalf("placed %d files with %d rejected, want none placed and 2 rejected", len(segments), rejected)
	}
	segments, _ = placeAudioCredits(creditsSeason(3, 60, 0))
	if len(segments) != 3 {
		t.Fatalf("placed %d files of three confirming episodes, want 3", len(segments))
	}
}

func TestCreditsAudioWithoutVideo(t *testing.T) {
	profile := creditsProfile()
	const duration = 1500.0
	match := func(start, end float64, confirmations int, consistent bool) seasonMatch {
		return seasonMatch{Segment: Segment{Start: start, End: end}, Confirmations: confirmations, SeasonConsistent: consistent}
	}
	cases := []struct {
		name  string
		match seasonMatch
		want  float64
	}{
		{"strong and season-consistent", match(1410, 1500, 3, true), 0.90},
		{"strong but not season-consistent", match(1410, 1500, 2, false), 0.65},
		{"one confirmation is weak", match(1410, 1500, 1, true), 0},
		{"short and not season-consistent", match(1485, 1500, 4, false), 0},
		{"short but season-consistent", match(1484, 1500, 4, true), 0.90},
		{"stops before the end of the file", match(1300, 1480, 4, true), 0},
		{"ends within the EOF snap", match(1300, 1486, 4, true), 0.90},
	}
	for _, tc := range cases {
		audio := creditsAudioFor(tc.match, profile)
		segment, ok := combineCredits(Candidate{DurationSeconds: duration}, creditsEvidence{Audio: &audio}, creditsLimitsFor(false))
		if ok != (tc.want > 0) {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.want > 0)
			continue
		}
		if ok && (segment.Confidence != tc.want || segment.Algorithm != CreditsAudioAlgorithm || segment.End != duration) {
			t.Errorf("%s: %+v, want confidence %.2f to the end of the file", tc.name, segment, tc.want)
		}
	}
}

func TestCreditsProfileSnapsToEOFNotChapters(t *testing.T) {
	profile := creditsProfile()
	candidate := Candidate{
		DurationSeconds: 1500,
		Chapters:        []models.MediaChapter{{StartSeconds: 1409, EndSeconds: 1500}},
	}
	got := adjustSegment(Segment{Start: 360, End: 440}, fingerprintInput{Candidate: candidate, WindowStart: 1050}, profile)
	if want := 1050 + 360 + chromaprintStartLeadSeconds; got.Start != want {
		t.Fatalf("start = %.2f, want %.2f with no chapter snap", got.Start, want)
	}
	if got.End != 1500 {
		t.Fatalf("end = %.2f, want the 1500 s file end", got.End)
	}
	// A start near the start of the window is not a start of the file.
	got = adjustSegment(Segment{Start: 0.2, End: 100}, fingerprintInput{Candidate: candidate, WindowStart: 1050}, profile)
	if got.Start == 0 {
		t.Fatal("credits starts must not snap to zero")
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
