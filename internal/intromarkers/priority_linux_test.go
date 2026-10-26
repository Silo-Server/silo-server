//go:build linux

package intromarkers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scheduled analysis runs ffmpeg at nice 19; analysis started from playback
// keeps the server's priority. The fake ffmpeg records its own nice value.
func TestAnalysisFFmpegPriority(t *testing.T) {
	candidate := Candidate{FileID: 1, FilePath: "/media/Show/S01E01.mkv", DurationSeconds: 1200}
	serverNice := readProcNice(t, "/proc/self/stat")
	if serverNice == "19" {
		t.Skip("the test already runs at nice 19")
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"scheduled", context.Background(), "19"},
		{"playback", WithPlaybackPriority(context.Background()), serverNice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "nice")
			// Field 19 of the stat line is nice; $$ is the fake ffmpeg.
			ffmpeg := writeFakeFFmpeg(t, fmt.Sprintf("cut -d' ' -f19 /proc/$$/stat >> %q\nprintf '\\001\\000\\000\\000'", out))
			cfg := DefaultConfig(ffmpeg)

			if _, _, err := NewChromaprintExtractor(cfg).Extract(tc.ctx, candidate); err != nil {
				t.Fatalf("Extract: %v", err)
			}
			segment := Segment{Start: 60, End: 120, Confidence: 0.95, Algorithm: ChapterAlgorithm}
			if _, _, err := NewSilenceBoundaryRefiner(cfg).RefineChapterEnd(tc.ctx, candidate, segment); err != nil {
				t.Fatalf("RefineChapterEnd: %v", err)
			}

			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Fields(string(data)), []string{tc.want, tc.want}; strings.Join(got, " ") != strings.Join(want, " ") {
				t.Fatalf("ffmpeg nice (fingerprint, silence) = %q, want %q", got, want)
			}
		})
	}
}

func readProcNice(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := string(data)
	return strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])[16]
}
