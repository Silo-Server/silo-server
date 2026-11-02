package intromarkers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSilenceBoundaryRefinerRefineChapterEnd(t *testing.T) {
	detect := func(lines ...string) string {
		var body strings.Builder
		for _, line := range lines {
			fmt.Fprintf(&body, "echo \"[silencedetect @ 0x1] %s\" >&2\n", line)
		}
		return body.String() + "exit 0"
	}
	tests := []struct {
		name    string
		script  string
		wantErr bool
		want    *Segment
	}{
		{
			name: "applies the first usable silence",
			script: detect("silence_start: 1", "silence_end: 1.2 | silence_duration: 0.2",
				"silence_start: 7", "silence_end: 8 | silence_duration: 1"),
			want: &Segment{Start: 60, End: 124, Confidence: 0.98, Algorithm: ChapterSilenceAlgorithm},
		},
		{
			name:   "accepts a silence adjacent to the boundary",
			script: detect("silence_start: 3.5", "silence_end: 4 | silence_duration: 0.5"),
			want:   &Segment{Start: 60, End: 120.5, Confidence: 0.98, Algorithm: ChapterSilenceAlgorithm},
		},
		{name: "rejects a too-small extension", script: detect("silence_start: 3.3", "silence_end: 3.8 | silence_duration: 0.5")},
		{name: "rejects unterminated silences", script: detect("silence_start: 1", "silence_start: 34")},
		{name: "returns the ffmpeg failure", script: "exit 1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refiner := NewSilenceBoundaryRefiner(DefaultConfig(writeFakeFFmpeg(t, tt.script)))
			segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
			candidate := Candidate{FileID: 1, FilePath: "/tmp/media.mkv", DurationSeconds: 1200}

			refined, ok, err := refiner.RefineChapterEnd(context.Background(), candidate, segment)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RefineChapterEnd error = %v, want error %t", err, tt.wantErr)
			}
			if ok != (tt.want != nil) {
				t.Fatalf("refined = %t (%+v), want %t", ok, refined, tt.want != nil)
			}
			if tt.want != nil && refined != *tt.want {
				t.Fatalf("refined segment %+v, want %+v", refined, *tt.want)
			}
		})
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
