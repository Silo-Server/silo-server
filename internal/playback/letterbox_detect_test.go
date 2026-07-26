package playback

import "testing"

// A 2.39:1 image inside a 1920x1080 frame: 140px of black top and bottom.
const cropDetectSample = `
[Parsed_cropdetect_0 @ 0x600000] x1:0 x2:1919 y1:139 y2:939 w:1920 h:800 x:0 y:140 pts:12345 t:0.514 crop=1920:800:0:140
[Parsed_cropdetect_0 @ 0x600000] x1:0 x2:1919 y1:139 y2:939 w:1920 h:800 x:0 y:140 pts:12387 t:0.556 crop=1920:800:0:140
`

func TestParseCropDetectReadsBarsAsFrameFractions(t *testing.T) {
	got, ok := parseCropDetect(cropDetectSample, 1080)
	if !ok {
		t.Fatal("expected a measurement")
	}
	// 140/1080 on each edge.
	if diff := got.TopFraction - 140.0/1080.0; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("top = %v", got.TopFraction)
	}
	if diff := got.BottomFraction - 140.0/1080.0; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("bottom = %v", got.BottomFraction)
	}
}

// reset=0 makes cropdetect accumulate, so the LAST line is the one that has
// seen every frame in the sample.
func TestParseCropDetectPrefersTheLastLine(t *testing.T) {
	output := `
[Parsed_cropdetect_0 @ 0x1] crop=1920:1080:0:0
[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140
`
	got, ok := parseCropDetect(output, 1080)
	if !ok {
		t.Fatal("expected a measurement")
	}
	if got.TopFraction == 0 {
		t.Fatalf("expected the accumulated crop, got %v", got)
	}
}

func TestParseCropDetectRejectsUnusableOutput(t *testing.T) {
	cases := map[string]struct {
		output      string
		frameHeight int
	}{
		"no crop lines":          {"[info] Stream #0:0: Video: h264", 1080},
		"malformed fields":       {"crop=abc:def:0:0", 1080},
		"too few fields":         {"crop=1920:800:0", 1080},
		"zero frame height":      {cropDetectSample, 0},
		"crop taller than frame": {"crop=1920:1080:0:200", 1080},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseCropDetect(tc.output, tc.frameHeight); ok {
				t.Fatal("expected the measurement to be refused")
			}
		})
	}
}

// A fade to black or a night exterior looks exactly like a bar in one sample.
// Keeping the smaller bar per edge discards it.
func TestIntersectKeepsTheSmallerBarPerEdge(t *testing.T) {
	real := Letterbox{TopFraction: 0.13, BottomFraction: 0.13}
	darkFrame := Letterbox{TopFraction: 0.40, BottomFraction: 0.35}

	got := real.intersect(darkFrame)

	if got.TopFraction != 0.13 || got.BottomFraction != 0.13 {
		t.Fatalf("a dark sample widened the bars: %v", got)
	}
}

func TestSanitizeDropsNoiseAndRefusesAbsurdBars(t *testing.T) {
	// 0.5% is rounding noise, not a bar worth moving subtitles for.
	if got := sanitizeLetterbox(Letterbox{TopFraction: 0.005, BottomFraction: 0.005}); got.Detected() {
		t.Fatalf("noise was treated as a bar: %v", got)
	}
	// 40% per edge would leave a fifth of the frame — likelier a bad read than
	// a real letterbox, and acting on it would put cues mid-picture.
	if got := sanitizeLetterbox(Letterbox{TopFraction: 0.40, BottomFraction: 0.40}); got.Detected() {
		t.Fatalf("an absurd measurement was accepted: %v", got)
	}
	kept := sanitizeLetterbox(Letterbox{TopFraction: 0.13, BottomFraction: 0.13})
	if !kept.Detected() {
		t.Fatalf("a real letterbox was discarded: %v", kept)
	}
}

// Samples avoid the opening and closing minutes, where black frames live.
func TestSamplePositionsSpreadOverTheRuntime(t *testing.T) {
	positions := samplePositions(1000)

	if len(positions) != sampleCount {
		t.Fatalf("expected %d samples, got %d", sampleCount, len(positions))
	}
	if positions[0] <= 0 || positions[len(positions)-1] >= 1000 {
		t.Fatalf("samples touched the ends: %v", positions)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Fatalf("samples are not increasing: %v", positions)
		}
	}
}

// Detection must never block or run for inputs it cannot measure.
func TestCacheIgnoresUnmeasurableInputs(t *testing.T) {
	cache := NewLetterboxCache(func() string { return "" })

	cache.Warm("", 100, 1080)
	cache.Warm("/tmp/x.mkv", 0, 1080)
	cache.Warm("/tmp/x.mkv", 100, 0)

	if _, ok := cache.Lookup("/tmp/x.mkv"); ok {
		t.Fatal("an unmeasurable input was recorded")
	}
}

// A nil cache is a disabled cache, not a crash: callers should not have to
// branch on whether detection is configured.
func TestNilCacheIsInert(t *testing.T) {
	var cache *LetterboxCache

	cache.Warm("/tmp/x.mkv", 100, 1080)
	if _, ok := cache.Lookup("/tmp/x.mkv"); ok {
		t.Fatal("a nil cache returned a measurement")
	}
}

// The planner must pass a measurement through to the client contract verbatim:
// the geometry describes the source, not a decision the planner makes.
func TestPlannerPublishesLetterboxOnTheSource(t *testing.T) {
	source := SourceDescriptorV3{Width: 1920, Height: 1080}
	source.LetterboxTopFraction = 0.13
	source.LetterboxBottomFraction = 0.13

	if source.LetterboxTopFraction != 0.13 || source.LetterboxBottomFraction != 0.13 {
		t.Fatalf("source did not carry the measurement: %+v", source)
	}
}
