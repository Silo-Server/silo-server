package intromarkers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// End-to-end check of the loudness-envelope path over real media.
//
// The episodes are generated with ffmpeg's own sources, so the test needs no
// fixture in the repository: the same studio logo and the same theme at a
// different offset in each episode, dialog around them from three different
// noise seeds, and a tail of black with text after the story. It is skipped
// without ffmpeg, and with -short.
func TestLevelsFindTheIntroInGeneratedEpisodes(t *testing.T) {
	if testing.Short() {
		t.Skip("generates media with ffmpeg")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()

	cfg := DefaultConfig("ffmpeg")
	cfg.AnalysisPercent = 100
	cfg.AnalysisLengthLimitMinutes = 10
	extractor := NewChromaprintExtractor(cfg)
	ctx := context.Background()

	// The theme and the logo are one file each, concatenated into every
	// episode so the audio is the same recording, not merely similar.
	logo := buildPart(t, dir, "logo", "color=c=0x101820:s=160x90:d=4,drawtext=fontfile="+testFont(t)+":text='STUDIO':fontcolor=white:fontsize=14:x=10:y=35", "sine=frequency=220:duration=4")
	theme := buildPart(t, dir, "theme",
		"color=c=0x203050:s=160x90:d=20,drawbox=x=20+mod(t*30\\,100):y=30:w=40:h=40:color=0xe0d0a0:t=fill",
		"sine=frequency=330:duration=20,volume=0.5")

	coldOpens := []int{8, 20, 4}
	storySeconds := []int{40, 28, 56}
	var inputs []levelsInput
	for i, cold := range coldOpens {
		episode := i + 1
		coldOpen := buildPart(t, dir, fmt.Sprintf("cold%d", episode),
			fmt.Sprintf("color=c=0x402818:s=160x90:d=%d,noise=alls=30:allf=t", cold),
			fmt.Sprintf("anoisesrc=r=44100:d=%d:amplitude=0.05:color=brown:seed=%d", cold, 100+episode))
		story := buildPart(t, dir, fmt.Sprintf("story%d", episode),
			fmt.Sprintf("color=c=0x285030:s=160x90:d=%d,noise=alls=18:allf=t+%d", storySeconds[i], episode),
			fmt.Sprintf("anoisesrc=r=44100:d=%d:amplitude=0.04:color=pink:seed=%d", storySeconds[i], 200+episode))
		path := filepath.Join(dir, fmt.Sprintf("Show.S01E0%d.mkv", episode))
		concatParts(t, path, logo, coldOpen, theme, story)
		duration := probeTestDuration(t, path)
		inputs = append(inputs, levelsInput{
			Levels:      extractTestLevels(t, extractor, ctx, path, duration),
			WindowStart: 0,
			Candidate: Candidate{
				FileID: episode, EpisodeID: fmt.Sprintf("ep%d", episode), EpisodeNumber: episode,
				SeasonID: "s1", FilePath: path, DurationSeconds: duration,
				CodecVideo: "h264", CodecAudio: "aac",
			},
		})
	}

	segments := CompareLevels(inputs, cfg)
	// The theme starts after the logo (4 s) and the cold open, and runs 20 s.
	for i, cold := range coldOpens {
		wantStart := 4 + float64(cold)
		wantEnd := wantStart + 20
		got, ok := segments[i+1]
		if !ok {
			t.Fatalf("episode %d: no intro found, want %s", i+1, segmentRange(wantStart, wantEnd))
		}
		if absFloat(got.Start-wantStart) > 2.5 || absFloat(got.End-wantEnd) > 2.5 {
			t.Fatalf("episode %d: intro %s, want %s", i+1, segmentRange(got.Start, got.End), segmentRange(wantStart, wantEnd))
		}
		if got.End-got.Start > 60 {
			t.Fatalf("episode %d: intro spans %.1fs, the match bridged the whole opening", i+1, got.End-got.Start)
		}
	}
}

// buildPart encodes one stretch of video and audio with fixed settings, so
// every part concatenates with the others.
func buildPart(t *testing.T, dir, name, video, audio string) string {
	t.Helper()
	path := filepath.Join(dir, name+".mp4")
	runFFmpeg(t, "-f", "lavfi", "-i", video, "-f", "lavfi", "-i", audio,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-r", "25", "-s", "160x90", "-g", "25", "-c:a", "aac", "-b:a", "64k", "-ar", "44100", "-ac", "1",
		"-shortest", path)
	return path
}

// concatParts joins parts into one file with the concat demuxer, so their
// audio and video are copied rather than re-encoded.
func concatParts(t *testing.T, path string, parts ...string) {
	t.Helper()
	list := path + ".txt"
	var lines strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&lines, "file '%s'\n", part)
	}
	if err := os.WriteFile(list, []byte(lines.String()), 0o600); err != nil {
		t.Fatalf("writing the concat list: %v", err)
	}
	runFFmpeg(t, "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", path)
}

// runFFmpeg runs ffmpeg with the arguments given after -y.
func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	if out, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// extractTestLevels measures a file's envelope as the analyzer does.
func extractTestLevels(t *testing.T, extractor *ChromaprintExtractor, ctx context.Context, path string, duration float64) []byte {
	t.Helper()
	input, ok, err := extractor.ExtractLevels(ctx, Candidate{FileID: 1, FilePath: path, DurationSeconds: duration})
	if err != nil || !ok {
		t.Fatalf("measuring the envelope of %s: ok=%v err=%v", path, ok, err)
	}
	return input.Levels
}

// probeTestDuration reads a file's duration.
func probeTestDuration(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("duration %q: %v", out, err)
	}
	return value
}

// testFont returns a font file drawtext can use, or skips the test.
func testFont(t *testing.T) string {
	t.Helper()
	for _, path := range []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/TTF/DejaVuSans.ttf",
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("no TrueType font for drawtext")
	return ""
}
