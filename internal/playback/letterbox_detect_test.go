package playback

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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

	cache.Warm("k", "", 100, 1080)
	cache.Warm("k", "/tmp/x.mkv", 0, 1080)
	cache.Warm("k", "/tmp/x.mkv", 100, 0)

	if _, ok := cache.Lookup("k"); ok {
		t.Fatal("an unmeasurable input was recorded")
	}
}

// Replacing a file in place — an upgrade, a re-encode — keeps its path. Serving
// the old file's bars would put subtitles in the wrong place with nothing a
// user could do about it.
func TestCacheKeyChangesWhenTheFileIsReplaced(t *testing.T) {
	same := LetterboxCacheKey("/media/film.mkv", 8_000_000_000)
	if LetterboxCacheKey("/media/film.mkv", 8_000_000_000) != same {
		t.Fatal("the same file produced two keys")
	}
	if LetterboxCacheKey("/media/film.mkv", 12_000_000_000) == same {
		t.Fatal("a replaced file reused the old measurement's key")
	}
}

// A long-lived server must not accumulate one entry per file it has ever
// played.
func TestCacheIsBounded(t *testing.T) {
	cache := NewLetterboxCache(func() string { return "" })

	for i := range maxLetterboxEntries + 10 {
		key := strconv.Itoa(i)
		cache.measured[key] = Letterbox{}
		cache.order = append(cache.order, key)
	}
	cache.evictLocked()

	if len(cache.measured) != maxLetterboxEntries || len(cache.order) != maxLetterboxEntries {
		t.Fatalf("cache held %d entries", len(cache.measured))
	}
	if _, ok := cache.measured["0"]; ok {
		t.Fatal("the oldest entry survived eviction")
	}
	if _, ok := cache.measured[strconv.Itoa(maxLetterboxEntries+9)]; !ok {
		t.Fatal("the newest entry was evicted")
	}
}

// A nil cache is a disabled cache, not a crash: callers should not have to
// branch on whether detection is configured.
func TestNilCacheIsInert(t *testing.T) {
	var cache *LetterboxCache

	cache.Warm("k", "/tmp/x.mkv", 100, 1080)
	if _, ok := cache.Lookup("k"); ok {
		t.Fatal("a nil cache returned a measurement")
	}
}

// The planner must pass a measurement through to the client contract verbatim:
// the geometry describes the source, not a decision the planner makes, so no
// route or delivery choice may alter or drop it.
func TestPlannerPublishesLetterboxOnTheSource(t *testing.T) {
	file := &models.MediaFile{
		ID: 7, FilePath: "/media/scope.mkv", Container: "mkv", CodecVideo: "h264", CodecAudio: "aac",
		Resolution: "1080p", Bitrate: 12_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 40, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}

	result := PlanPlaybackV3(PlannerInputV3{
		Request:       validStartRequestV3(),
		RequestedFile: file,
		EffectiveFile: file,
		Settings:      PlannerSettingsV3{TranscodeEnabled: true},
		Registry:      testTransformationRegistryV3(),
		Letterbox:     Letterbox{TopFraction: 0.1287, BottomFraction: 0.1287},
	})

	if result.Plan == nil {
		t.Fatalf("no plan produced: %#v", result)
	}
	if result.Plan.Source.LetterboxTopFraction != 0.1287 ||
		result.Plan.Source.LetterboxBottomFraction != 0.1287 {
		t.Fatalf("plan did not carry the measurement: %+v", result.Plan.Source)
	}
}

// No measurement must leave the fields off the wire entirely, so a client can
// tell "not measured" from a real zero without a sentinel.
func TestPlannerOmitsAnUnmeasuredLetterbox(t *testing.T) {
	source := SourceDescriptorV3{Width: 1920, Height: 1080}

	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("letterbox")) {
		t.Fatalf("an unmeasured source published letterbox fields: %s", encoded)
	}
}
