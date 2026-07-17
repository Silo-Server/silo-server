package playback

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestIsPGS(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		{"pgs", true},
		{"hdmv_pgs_subtitle", true},
		{"HDMV_PGS_SUBTITLE", true},
		{"dvd_subtitle", false},
		{"dvb_subtitle", false},
		{"subrip", false},
		{"ass", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsPGS(tc.codec); got != tc.want {
			t.Errorf("IsPGS(%q) = %v, want %v", tc.codec, got, tc.want)
		}
	}
}

func TestStreamExtractOutput(t *testing.T) {
	cases := []struct {
		codec      string
		wantCodec  string
		wantFormat string
	}{
		{"ass", "copy", "ass"},
		{"ssa", "copy", "ass"},
		{"pgs", "copy", "sup"},
		{"hdmv_pgs_subtitle", "copy", "sup"},
		{"subrip", "webvtt", "webvtt"},
		{"mov_text", "webvtt", "webvtt"},
	}
	for _, tc := range cases {
		outCodec, outFormat := streamExtractOutput(tc.codec)
		if outCodec != tc.wantCodec || outFormat != tc.wantFormat {
			t.Errorf("streamExtractOutput(%q) = (%q, %q), want (%q, %q)",
				tc.codec, outCodec, outFormat, tc.wantCodec, tc.wantFormat)
		}
	}
}

func TestStreamExtractArgs_TextCodecIsWindowed(t *testing.T) {
	args := streamExtractArgs(StreamExtractOpts{
		InputPath:       "/media/movie.mkv",
		TrackIndex:      2,
		SourceCodec:     "subrip",
		SeekSeconds:     120,
		DurationSeconds: 600,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-ss 120.000") {
		t.Fatalf("text extract should seek the input: %s", joined)
	}
	if !strings.Contains(joined, "-t 600.000") {
		t.Fatalf("text extract should cap the read duration: %s", joined)
	}
	if !strings.Contains(joined, "-copyts") {
		t.Fatalf("seeked extract must preserve source timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-c:s webvtt") || !strings.Contains(joined, "-f webvtt") {
		t.Fatalf("text extract should transmux to WebVTT: %s", joined)
	}
}

// ASS and PGS streams are fetched once and consumed whole by their
// client-side renderers, so by default seek/duration windowing must never
// apply even when the handler passes nonzero values.
func TestStreamExtractArgs_WholeTrackCodecsIgnoreWindow(t *testing.T) {
	for _, codec := range []string{"ass", "hdmv_pgs_subtitle"} {
		args := streamExtractArgs(StreamExtractOpts{
			InputPath:       "/media/movie.mkv",
			TrackIndex:      0,
			SourceCodec:     codec,
			SeekSeconds:     120,
			DurationSeconds: 600,
		})

		if slices.Contains(args, "-ss") {
			t.Errorf("%s extract must not seek the input: %v", codec, args)
		}
		if slices.Contains(args, "-t") {
			t.Errorf("%s extract must not cap the read duration: %v", codec, args)
		}
		if !slices.Contains(args, "copy") {
			t.Errorf("%s extract must copy the source stream: %v", codec, args)
		}
	}
}

// A client that opts in via AllowWindow gets a seeked, duration-capped PGS
// extract with -copyts preserving absolute source timestamps — the -ss must
// be an input option (before -i) so ffmpeg uses the container index.
func TestStreamExtractArgs_WindowedPGS(t *testing.T) {
	args := streamExtractArgs(StreamExtractOpts{
		InputPath:       "/media/movie.mkv",
		TrackIndex:      1,
		SourceCodec:     "hdmv_pgs_subtitle",
		SeekSeconds:     1200,
		DurationSeconds: 3600,
		AllowWindow:     true,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-ss 1200.000") {
		t.Fatalf("windowed PGS extract should seek the input: %s", joined)
	}
	ssIdx := slices.Index(args, "-ss")
	inIdx := slices.Index(args, "-i")
	if ssIdx < 0 || inIdx < 0 || ssIdx > inIdx {
		t.Fatalf("-ss must be an input option (before -i): %s", joined)
	}
	if !strings.Contains(joined, "-t 3600.000") {
		t.Fatalf("windowed PGS extract should cap the read duration: %s", joined)
	}
	if !strings.Contains(joined, "-copyts") {
		t.Fatalf("windowed PGS extract must preserve source timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-c:s copy") || !strings.Contains(joined, "-f sup pipe:1") {
		t.Fatalf("windowed PGS extract should still copy into a sup stream: %s", joined)
	}
}

// A windowed extract whose input is a cached full-track .sup must force the
// sup demuxer (the elementary stream has no container header to probe from
// arbitrary offsets), remap to the file's sole stream regardless of the
// original container's track ordinal, and still seek/window with -copyts so
// the cached stream's absolute timestamps survive into the output.
func TestStreamExtractArgs_ExtractedSupInput(t *testing.T) {
	args := streamExtractArgs(StreamExtractOpts{
		InputPath:           "/transcode/subtitle-cache/abc-s3-1-2.sup",
		TrackIndex:          3,
		SourceCodec:         "hdmv_pgs_subtitle",
		SeekSeconds:         1200,
		DurationSeconds:     3600,
		AllowWindow:         true,
		InputIsExtractedSup: true,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-f sup -i /transcode/subtitle-cache/abc-s3-1-2.sup") {
		t.Fatalf("cached sup input must force the sup demuxer before -i: %s", joined)
	}
	if !strings.Contains(joined, "-map 0:s:0") {
		t.Fatalf("cached sup holds exactly one stream; must map 0:s:0: %s", joined)
	}
	if strings.Contains(joined, "0:s:3") {
		t.Fatalf("original container track ordinal must not leak into sup input mapping: %s", joined)
	}
	if !strings.Contains(joined, "-ss 1200.000") || !strings.Contains(joined, "-t 3600.000") {
		t.Fatalf("cached sup extract must still window the input: %s", joined)
	}
	ssIdx := slices.Index(args, "-ss")
	inIdx := slices.Index(args, "-i")
	if ssIdx < 0 || inIdx < 0 || ssIdx > inIdx {
		t.Fatalf("-ss must be an input option (before -i): %s", joined)
	}
	if !strings.Contains(joined, "-copyts") {
		t.Fatalf("cached sup extract must preserve absolute timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-c:s copy") || !strings.Contains(joined, "-f sup pipe:1") {
		t.Fatalf("cached sup extract should copy into a sup stream: %s", joined)
	}
}

// AllowWindow must not override the ASS guard — its [Script Info] header
// only exists at stream offset 0, so a seeked extract would be broken.
func TestStreamExtractArgs_ASSIgnoresAllowWindow(t *testing.T) {
	args := streamExtractArgs(StreamExtractOpts{
		InputPath:       "/media/movie.mkv",
		TrackIndex:      0,
		SourceCodec:     "ass",
		SeekSeconds:     120,
		DurationSeconds: 600,
		AllowWindow:     true,
	})

	if slices.Contains(args, "-ss") {
		t.Errorf("ass extract must not seek the input even with AllowWindow: %v", args)
	}
	if slices.Contains(args, "-t") {
		t.Errorf("ass extract must not cap the read duration even with AllowWindow: %v", args)
	}
}

// Absent the explicit ?windowed=1 opt-in the request must not window, no
// matter what other params are present — existing clients (Apple, Android,
// jellycompat) send no param and rely on whole-track extraction.
func TestPGSWindowRequest(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		wantAllow    bool
		wantSeek     float64
		wantDuration float64
	}{
		{"no params", "", false, 0, 0},
		{"position without opt-in", "position=120&duration=600", false, 0, 0},
		{"windowed off", "windowed=0&position=120", false, 0, 0},
		{"opt-in with position and duration", "windowed=1&position=120.5&duration=3600", true, 120.5, 3600},
		{"opt-in without position", "windowed=1", true, 0, 0},
		{"opt-in negative position ignored", "windowed=1&position=-5&duration=600", true, 0, 600},
		{"opt-in duration over cap ignored", "windowed=1&position=10&duration=7200", true, 10, 0},
		{"opt-in invalid values ignored", "windowed=1&position=abc&duration=xyz", true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.query, err)
			}
			allow, seek, duration := PGSWindowRequest(q)
			if allow != tc.wantAllow || seek != tc.wantSeek || duration != tc.wantDuration {
				t.Errorf("PGSWindowRequest(%q) = (%v, %v, %v), want (%v, %v, %v)",
					tc.query, allow, seek, duration, tc.wantAllow, tc.wantSeek, tc.wantDuration)
			}
		})
	}
}

// A forced "vtt" target applies only to text sources: bitmap codecs carry no
// text for ffmpeg's webvtt encoder, so the override must fall back to the
// source-driven mapping instead of building a command that always fails.
func TestStreamExtractOutput_TargetFormatVTTGatedToTextSources(t *testing.T) {
	cases := []struct {
		codec      string
		wantCodec  string
		wantFormat string
	}{
		{"subrip", "webvtt", "webvtt"},
		{"mov_text", "webvtt", "webvtt"},
		{"ass", "webvtt", "webvtt"},
		{"pgs", "copy", "sup"},
		{"hdmv_pgs_subtitle", "copy", "sup"},
	}
	for _, tc := range cases {
		outCodec, outFormat := streamExtractOutput(tc.codec, "vtt")
		if outCodec != tc.wantCodec || outFormat != tc.wantFormat {
			t.Errorf("streamExtractOutput(%q, \"vtt\") = (%q, %q), want (%q, %q)",
				tc.codec, outCodec, outFormat, tc.wantCodec, tc.wantFormat)
		}
	}
}

func TestStreamExtractArgs_PGSProducesSup(t *testing.T) {
	args := streamExtractArgs(StreamExtractOpts{
		InputPath:   "/media/movie.mkv",
		TrackIndex:  1,
		SourceCodec: "pgs",
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:s:1 -c:s copy -f sup pipe:1") {
		t.Fatalf("PGS extract should copy into a sup stream: %s", joined)
	}
}
