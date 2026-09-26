package intromarkers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
