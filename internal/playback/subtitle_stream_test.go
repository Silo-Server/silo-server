package playback

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStreamExtractSubtitleBoundsWindow(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to verify subtitle window timestamps")
	}
	source := filepath.Join(t.TempDir(), "captions.srt")
	const input = "1\n00:00:00,000 --> 00:00:05,000\nBefore window\n\n2\n00:13:20,000 --> 00:13:25,000\nInside window\n\n3\n00:16:40,000 --> 00:16:45,000\nAfter window\n"
	if err := os.WriteFile(source, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := StreamExtractSubtitle(t.Context(), StreamExtractOpts{
		InputPath: source, SourceCodec: "subrip", SeekSeconds: 800, DurationSeconds: 100,
		FFmpegPath: bin, Writer: &output,
	}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Inside window") ||
		!strings.Contains(got, "13:20.000 --> 13:25.000") ||
		strings.Contains(got, "Before window") || strings.Contains(got, "After window") {
		t.Fatalf("expected only the in-window cue at its source timestamp, got %q", got)
	}
}

// The playback timeline starts at the container start. A window cut after a
// seek must put each cue where the unseeked extract does, even when the
// container's first timestamp is not zero (TS remuxes), or subtitles run late
// by that start time after every seek.
func TestStreamExtractSubtitleSeekKeepsContainerRelativeTimeline(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to verify subtitle window timestamps")
	}
	dir := t.TempDir()
	srt := filepath.Join(dir, "captions.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:40,000 --> 00:00:42,000\nForty seconds in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "offset.mkv")
	mux := exec.CommandContext(t.Context(), bin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:r=5:d=60", "-i", srt,
		"-map", "0:v", "-map", "1:s", "-c:v", "mpeg4", "-c:s", "srt",
		"-output_ts_offset", "30", source)
	if out, err := mux.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, out)
	}

	extract := func(seek float64) string {
		t.Helper()
		var output bytes.Buffer
		if err := StreamExtractSubtitle(t.Context(), StreamExtractOpts{
			InputPath: source, SourceCodec: "subrip", SeekSeconds: seek, DurationSeconds: 600,
			FFmpegPath: bin, Writer: &output,
		}); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	const cue = "00:40.000 --> 00:42.000"
	if got := extract(0); !strings.Contains(got, cue) {
		t.Fatalf("unseeked extract should place the cue at %s, got %q", cue, got)
	}
	if got := extract(35); !strings.Contains(got, cue) {
		t.Fatalf("seeked window should place the cue at %s like the unseeked extract, got %q", cue, got)
	}
}

type failingSubtitleWriter struct{ err error }

func (w failingSubtitleWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStreamExtractSubtitleStopsOnWriterFailure(t *testing.T) {
	// Keep the extractor producing output after the response writer fails.
	// Waiting for it without canceling first leaves it blocked on a full pipe.
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec cat /dev/zero\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	writeErr := errors.New("response writer disconnected")
	err := StreamExtractSubtitle(ctx, StreamExtractOpts{
		InputPath: "unused.mkv", FFmpegPath: bin, Writer: failingSubtitleWriter{writeErr},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("extract error = %v, want writer error %v", err, writeErr)
	}
	if ctx.Err() != nil {
		t.Fatalf("extract required request timeout to stop: %v", ctx.Err())
	}
}

func TestIsPGS(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		{"pgs", true},
		{"hdmv_pgs_subtitle", true},
		{"pgssub", true},
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
		{"pgssub", "copy", "sup"},
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
		if slices.Contains(args, "-to") {
			t.Errorf("%s extract must not cap the read duration: %v", codec, args)
		}
		if !slices.Contains(args, "copy") {
			t.Errorf("%s extract must copy the source stream: %v", codec, args)
		}
	}
}

// A client that opts in via AllowWindow gets a seeked, duration-capped PGS
// extract with -copyts keeping cues on the playback timeline — the -ss must
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
	if !strings.Contains(joined, "-to 4800.000") {
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
// the cached stream's stored timestamps survive into the output.
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
	// The seek starts early so a display set at the window start is read
	// whole; the end is unchanged.
	if !strings.Contains(joined, "-ss 1198.000") || !strings.Contains(joined, "-to 4800.000") {
		t.Fatalf("cached sup extract must still window the input: %s", joined)
	}
	ssIdx := slices.Index(args, "-ss")
	inIdx := slices.Index(args, "-i")
	if ssIdx < 0 || inIdx < 0 || ssIdx > inIdx {
		t.Fatalf("-ss must be an input option (before -i): %s", joined)
	}
	if !strings.Contains(joined, "-copyts") {
		t.Fatalf("cached sup extract must preserve its stored timestamps: %s", joined)
	}
	// The cached .sup is already on the playback timeline and its start time
	// is its first cue; rebasing it would pull every cue early by that much.
	if slices.Contains(args, "-start_at_zero") {
		t.Fatalf("cached sup extract must not rebase its timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-c:s copy") || !strings.Contains(joined, "-f sup pipe:1") {
		t.Fatalf("cached sup extract should copy into a sup stream: %s", joined)
	}
	// ffmpeg offsets an input seek by the input's start time, which for a
	// cached .sup is its first cue; the seek must be absolute instead.
	if tsIdx := slices.Index(args, "-seek_timestamp"); tsIdx < 0 || tsIdx > inIdx || args[tsIdx+1] != "1" {
		t.Fatalf("cached sup seek must be an absolute timestamp (-seek_timestamp 1 before -i): %s", joined)
	}
}

// An unseeked window from a cached .sup keeps its stored timestamps too:
// without -copyts ffmpeg rebases the stream onto its first cue.
func TestStreamExtractArgs_ExtractedSupInputFromStart(t *testing.T) {
	args := streamExtractArgs(StreamExtractOpts{
		InputPath:           "/transcode/subtitle-cache/abc-s3-1-2.sup",
		SourceCodec:         "hdmv_pgs_subtitle",
		DurationSeconds:     600,
		AllowWindow:         true,
		InputIsExtractedSup: true,
	})
	joined := strings.Join(args, " ")
	if !slices.Contains(args, "-copyts") || slices.Contains(args, "-start_at_zero") {
		t.Fatalf("cached sup extract must keep its stored timestamps: %s", joined)
	}
	if slices.Contains(args, "-ss") || slices.Contains(args, "-seek_timestamp") {
		t.Fatalf("an extract from the start must not seek: %s", joined)
	}
	if !strings.Contains(joined, "-to 600.000") {
		t.Fatalf("the window still ends at its duration: %s", joined)
	}
}

// AllowWindow must not truncate the script consumed by an ASS renderer that
// fetches the complete event timeline once.
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
	if slices.Contains(args, "-to") {
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

// A window from a cached .sup that starts at or before the first cue cannot
// be seeked (ffmpeg fails the seek and loses the first presentation
// segment), so it reads from the start and keeps its absolute end.
func TestWindowFromCachedSup(t *testing.T) {
	for _, tc := range []struct {
		name              string
		seek, duration    float64
		wantSeek, wantDur float64
	}{
		{name: "before the first cue", seek: 35, duration: 60, wantSeek: 0, wantDur: 95},
		{name: "onto the first cue", seek: 40, duration: 60, wantSeek: 0, wantDur: 100},
		{name: "after the first cue", seek: 95, duration: 60, wantSeek: 95, wantDur: 60},
		{name: "from the start", seek: 0, duration: 60, wantSeek: 0, wantDur: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := windowFromCachedSup(StreamExtractOpts{SeekSeconds: tc.seek, DurationSeconds: tc.duration}, 40)
			if got.SeekSeconds != tc.wantSeek || got.DurationSeconds != tc.wantDur {
				t.Fatalf("got seek %v duration %v, want %v %v", got.SeekSeconds, got.DurationSeconds, tc.wantSeek, tc.wantDur)
			}
		})
	}
}

func TestReadSupFirstCue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.sup")
	if err := os.WriteFile(path, testSupStream([]float64{91.88, 100}, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSupFirstCue(path)
	if err != nil || got != 91.88 {
		t.Fatalf("readSupFirstCue = %v, %v; want 91.88", got, err)
	}
	if err := os.WriteFile(path, []byte("not a sup stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupFirstCue(path); err == nil {
		t.Fatal("a file without the PG magic should be refused")
	}
}
