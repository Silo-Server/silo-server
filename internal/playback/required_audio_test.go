package playback

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestHLSRequiresNegotiatedAudioTrack(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe := ffprobePathFromFFmpeg(ffmpeg)
	if _, err := exec.LookPath(ffprobe); err != nil {
		t.Skip("ffprobe is not installed beside ffmpeg")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mkv")
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=size=64x64:rate=10:duration=1", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-c:v", "mpeg4", "-c:a", "ac3", "-shortest", source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, output)
	}
	silent := filepath.Join(dir, "silent.mkv")
	if output, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", source, "-map", "0:v:0", "-c", "copy", silent).CombinedOutput(); err != nil {
		t.Fatalf("silent fixture: %v: %s", err, output)
	}
	for _, tc := range []struct {
		name                                   string
		index                                  int
		required, wantError, wantAudio, silent bool
	}{
		{name: "missing negotiated ordinal", index: 1, required: true, wantError: true},
		{name: "present negotiated AC3", required: true, wantAudio: true},
		{name: "legacy missing ordinal", index: 1},
		{name: "zero default video only", silent: true},
		{name: "legacy default video only", index: -1, silent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := source
			if tc.silent {
				input = silent
			}
			opts := TranscodeOpts{InputPath: input, OutputDir: t.TempDir(), TargetCodecVideo: "copy", TargetCodecAudio: "copy", SourceVideoCodec: "mpeg4", AudioTrackIndex: tc.index, RequireAudioTrack: tc.required, SegmentDuration: 1}
			output, err := exec.CommandContext(ctx, ffmpeg, buildFFmpegArgs(opts)...).CombinedOutput()
			if tc.wantError {
				if err == nil {
					t.Fatal("missing negotiated audio produced successful video-only HLS")
				}
				return
			}
			if err != nil {
				t.Fatalf("HLS: %v: %s", err, output)
			}
			probe, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", filepath.Join(opts.OutputDir, "init.mp4")).CombinedOutput()
			if err != nil {
				t.Fatalf("probe: %v: %s", err, probe)
			}
			if got := strings.Contains(string(probe), "ac3"); got != tc.wantAudio {
				t.Fatalf("AC3 present=%v, want %v: %s", got, tc.wantAudio, probe)
			}
		})
	}
}

func TestRecipeCardRetainsRequiredAudio(t *testing.T) {
	card := NewRecipeCard(1, "profile", 1, "", TranscodeOpts{AudioTrackIndex: 1, RequireAudioTrack: true})
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	rebuilt := card.TranscodeOpts(t.TempDir(), "", nil)
	if !rebuilt.RequireAudioTrack || rebuilt.AudioTrackIndex != 1 {
		t.Fatal("recipe lost required audio selection")
	}
	claims := card.ToClaims()
	token, err := streamtoken.Sign(claims, "test-secret", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := streamtoken.Verify(token, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	restored := RecipeCardFromClaims(verified).TranscodeOpts(t.TempDir(), "", nil)
	if !restored.RequireAudioTrack || restored.AudioTrackIndex != 1 {
		t.Fatal("claims lost required audio selection")
	}
}
