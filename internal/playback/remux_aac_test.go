package playback

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func remuxAudioBitstreamFilterArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-bsf:a" {
			return args[i+1]
		}
	}
	return ""
}

func TestBuildRemuxArgsConvertsADTSOnlyForCopiedAAC(t *testing.T) {
	for _, test := range []struct {
		name           string
		transcodeAudio bool
		codec          string
		want           string
	}{
		{name: "copied aac", codec: "aac", want: "aac_adtstoasc"},
		{name: "copied aac any case", codec: " AAC ", want: "aac_adtstoasc"},
		{name: "copied eac3 would be rejected by the filter", codec: "eac3", want: ""},
		{name: "copied latm is not adts", codec: "aac_latm", want: ""},
		{name: "unknown codec from an older token", codec: "", want: ""},
		{name: "re-encoded aac is never adts", transcodeAudio: true, codec: "aac", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := buildRemuxArgsWithLeadingPictureDropV3("/x.ts", "mp4", 0, test.transcodeAudio, -1, 0, false, false, 0, 0, 0, false, test.codec)
			if got := remuxAudioBitstreamFilterArg(args); got != test.want {
				t.Fatalf("-bsf:a = %q, want %q (args=%s)", got, test.want, strings.Join(args, " "))
			}
		})
	}
}

// MPEG-TS carries AAC as ADTS. Under delay_moov FFmpeg does not insert
// aac_adtstoasc on its own, so a copy into fMP4 fails on the first audio
// packet and the client receives little more than the file header. The MKV
// case pins that the filter is harmless for AAC that is already raw.
func TestRemuxCopiesAACIntoFragmentedMP4(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobePath := ffprobePathFromFFmpeg(ffmpegPath)
	if _, err := exec.LookPath(ffprobePath); err != nil {
		t.Skip("ffprobe is not installed beside ffmpeg")
	}

	dir := t.TempDir()
	for _, test := range []struct {
		name string
		ext  string
		seek float64
	}{
		{name: "mpegts from start", ext: ".ts"},
		{name: "mpegts resumed", ext: ".ts", seek: 4},
		{name: "matroska", ext: ".mkv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourcePath := filepath.Join(dir, "source"+test.ext)
			encodeCtx, cancelEncode := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancelEncode()
			encode := exec.CommandContext(encodeCtx, ffmpegPath,
				"-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25",
				"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-t", "8",
				"-c:v", "mpeg4", "-g", "25",
				"-c:a", "aac", "-ac", "2",
				"-y", sourcePath,
			)
			if output, err := encode.CombinedOutput(); err != nil {
				t.Fatalf("generate AAC fixture: %v\n%s", err, output)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			outputPath := filepath.Join(dir, strings.ReplaceAll(test.name, " ", "-")+".mp4")
			args := buildRemuxArgsWithLeadingPictureDropV3(sourcePath, "mp4", test.seek, false, -1, 0, false, false, 0, 0, 0, false, "aac")
			args[len(args)-1] = outputPath
			if output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg remux failed: %v\n%s", err, output)
			}

			probe, err := exec.CommandContext(ctx, ffprobePath, "-v", "error",
				"-show_entries", "stream=codec_type:format=duration", "-of", "default=noprint_wrappers=1", outputPath).Output()
			if err != nil {
				t.Fatalf("probe remux output: %v", err)
			}
			if !strings.Contains(string(probe), "codec_type=audio") {
				t.Fatalf("remux output has no audio stream:\n%s", probe)
			}
			var duration float64
			for line := range strings.SplitSeq(string(probe), "\n") {
				if value, ok := strings.CutPrefix(line, "duration="); ok {
					duration, _ = strconv.ParseFloat(value, 64)
				}
			}
			if want := 8 - test.seek - 1; duration < want {
				t.Fatalf("remux output duration = %.2fs, want at least %.2fs:\n%s", duration, want, probe)
			}
		})
	}
}
