package intromarkers

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSilenceDetectOutput(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		windowStart float64
		want        []silenceInterval
	}{
		{
			name: "multiple pairs with a malformed line",
			output: `
[silencedetect @ 0x1] silence_start: 19.351
[silencedetect @ 0x1] silence_end: 29.319 | silence_duration: 9.968
malformed silence_start: nope
[silencedetect @ 0x1] silence_start: 31
[silencedetect @ 0x1] silence_end: 31.5 | silence_duration: 0.5
`,
			windowStart: 165,
			want:        []silenceInterval{{Start: 184.351, End: 194.319}, {Start: 196, End: 196.5}},
		},
		{
			name: "negative first start is clamped and keeps later pairs aligned",
			output: `
[silencedetect @ 0x1] silence_start: -0.000833333
[silencedetect @ 0x1] silence_end: 1.110354 | silence_duration: 1.111187
[silencedetect @ 0x1] silence_start: 4.307479
[silencedetect @ 0x1] silence_end: 5.506333 | silence_duration: 1.198854
`,
			windowStart: 100,
			want:        []silenceInterval{{Start: 100, End: 101.110354}, {Start: 104.307479, End: 105.506333}},
		},
		{
			name: "trailing open silence keeps its start",
			output: `
[silencedetect @ 0x1] silence_start: 2
[silencedetect @ 0x1] silence_end: 3 | silence_duration: 1
[silencedetect @ 0x1] silence_start: 8.5
`,
			windowStart: 10,
			want:        []silenceInterval{{Start: 12, End: 13}, {Start: 18.5}},
		},
		{
			name: "end without a start is ignored",
			output: `
[silencedetect @ 0x1] silence_end: 1 | silence_duration: 1
[silencedetect @ 0x1] silence_start: 2
[silencedetect @ 0x1] silence_end: 3 | silence_duration: 1
`,
			want: []silenceInterval{{Start: 2, End: 3}},
		},
		{
			name: "end pairs with the most recent unmatched start",
			output: `
[silencedetect @ 0x1] silence_start: 2
[silencedetect @ 0x1] silence_start: 4
[silencedetect @ 0x1] silence_end: 5 | silence_duration: 1
`,
			want: []silenceInterval{{Start: 2}, {Start: 4, End: 5}},
		},
		{
			name:   "exponent and carriage-return separated values",
			output: "size=N/A time=00:00:01.00\r[silencedetect @ 0x1] silence_start: 1.5e+01\r[silencedetect @ 0x1] silence_end: 1.6e1 | silence_duration: 1\n",
			want:   []silenceInterval{{Start: 15, End: 16}},
		},
		{
			// Excerpt of jellyfin-ffmpeg 7.1.4 stderr for a window that opens in
			// silence, with interleaved blackframe and metadata filter lines.
			name: "real jellyfin-ffmpeg excerpt",
			output: `
        encoder         : Lavc61.19.101 pcm_s16le
[silencedetect @ 0x7a76c4001100] silence_start: -0.000833333
[silencedetect @ 0x7a76c4001100] silence_end: 1.110354 | silence_duration: 1.111187
[silencedetect @ 0x7a76c4001100] silence_start: 4.307479
[silencedetect @ 0x7a76c4001100] silence_end: 5.506333 | silence_duration: 1.198854
[Parsed_blackframe_3 @ 0x7a76b800f9c0] frame:0 pblack:42 pts:7222 t:7.222000 type:I last_keyframe:0
[Parsed_metadata_5 @ 0x7a76b800fe40] lavfi.signalstats.VBITDEPTH=8
[silencedetect @ 0x7a76c4001100] silence_start: 40.358417
[Parsed_blackframe_3 @ 0x7a76b800f9c0] frame:4 pblack:19 pts:34015 t:34.015000 type:I last_keyframe:4
[silencedetect @ 0x7a76c4001100] silence_end: 40.973208 | silence_duration: 0.614792
[Parsed_metadata_5 @ 0x7a76b800fe40] frame:4    pts:34015   pts_time:34.015
`,
			windowStart: 1200,
			want: []silenceInterval{
				{Start: 1200, End: 1201.110354},
				{Start: 1204.307479, End: 1205.506333},
				{Start: 1240.358417, End: 1240.973208},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSilenceDetectOutput([]byte(tt.output), tt.windowStart)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d intervals %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if math.Abs(got[i].Start-tt.want[i].Start) > 1e-9 || math.Abs(got[i].End-tt.want[i].End) > 1e-9 {
					t.Fatalf("interval %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseSilenceDetectOutputEmpty(t *testing.T) {
	intervals := parseSilenceDetectOutput([]byte("no silence here"), 100)
	if len(intervals) != 0 {
		t.Fatalf("expected no intervals, got %d", len(intervals))
	}
}

func TestSilenceBoundaryRefinerAppliesFirstUsableSilence(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
echo "[silencedetect @ 0x1] silence_start: 1" >&2
echo "[silencedetect @ 0x1] silence_end: 1.2 | silence_duration: 0.2" >&2
echo "[silencedetect @ 0x1] silence_start: 7" >&2
echo "[silencedetect @ 0x1] silence_end: 8 | silence_duration: 1" >&2
exit 0
`)
	refiner := NewSilenceBoundaryRefiner(DefaultConfig(ffmpeg))
	segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
	candidate := Candidate{FileID: 1, FilePath: "/tmp/media.mkv", DurationSeconds: 1200}

	refined, ok, err := refiner.RefineChapterEnd(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChapterEnd returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected refinement")
	}
	if refined.End != 124 || refined.Algorithm != ChapterSilenceAlgorithm || refined.Confidence != 0.98 {
		t.Fatalf("unexpected refined segment: %+v", refined)
	}
}

func TestSilenceBoundaryRefinerAcceptsBoundaryAdjacentSilence(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
echo "[silencedetect @ 0x1] silence_start: 3.5" >&2
echo "[silencedetect @ 0x1] silence_end: 4 | silence_duration: 0.5" >&2
exit 0
`)
	refiner := NewSilenceBoundaryRefiner(DefaultConfig(ffmpeg))
	segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
	candidate := Candidate{FileID: 1, FilePath: "/tmp/media.mkv", DurationSeconds: 1200}

	refined, ok, err := refiner.RefineChapterEnd(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChapterEnd returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected boundary-adjacent silence to refine")
	}
	if refined.End != 120.5 {
		t.Fatalf("unexpected refined end %.1f", refined.End)
	}
}

func TestSilenceBoundaryRefinerRejectsTooSmallExtension(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
echo "[silencedetect @ 0x1] silence_start: 3.3" >&2
echo "[silencedetect @ 0x1] silence_end: 3.8 | silence_duration: 0.5" >&2
exit 0
`)
	refiner := NewSilenceBoundaryRefiner(DefaultConfig(ffmpeg))
	segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
	candidate := Candidate{FileID: 1, FilePath: "/tmp/media.mkv", DurationSeconds: 1200}

	refined, ok, err := refiner.RefineChapterEnd(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChapterEnd returned error: %v", err)
	}
	if ok {
		t.Fatalf("expected no refinement for sub-minimum extension, got %+v", refined)
	}
}

func TestSilenceBoundaryRefinerRejectsUnsafeSilence(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `
echo "[silencedetect @ 0x1] silence_start: 1" >&2
echo "[silencedetect @ 0x1] silence_start: 34" >&2
exit 0
`)
	refiner := NewSilenceBoundaryRefiner(DefaultConfig(ffmpeg))
	segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
	candidate := Candidate{FileID: 1, FilePath: "/tmp/media.mkv", DurationSeconds: 1200}

	refined, ok, err := refiner.RefineChapterEnd(context.Background(), candidate, segment)
	if err != nil {
		t.Fatalf("RefineChapterEnd returned error: %v", err)
	}
	if ok {
		t.Fatalf("expected no refinement, got %+v", refined)
	}
}

func TestSilenceBoundaryRefinerReturnsErrorOnFFmpegFailure(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, `exit 1`)
	refiner := NewSilenceBoundaryRefiner(DefaultConfig(ffmpeg))
	segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
	candidate := Candidate{FileID: 1, FilePath: "/tmp/media.mkv", DurationSeconds: 1200}

	_, ok, err := refiner.RefineChapterEnd(context.Background(), candidate, segment)
	if err == nil {
		t.Fatal("expected ffmpeg error")
	}
	if ok {
		t.Fatal("failed ffmpeg should not report refinement")
	}
}

func writeFakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("writing fake ffmpeg: %v", err)
	}
	return path
}
