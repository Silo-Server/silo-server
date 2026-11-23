package playback

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
			args := buildRemuxArgsWithLeadingPictureDropV3(sourcePath, "mp4", test.seek, false, -1, 0, false, false, 0, 0, 0, false, "aac", nil)
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

// An audio-only remux has no video keyframes for frag_keyframe to cut at, so
// without a fragment duration FFmpeg writes the whole book as one fragment
// once the conversion ends, and a client waits that long for its first audio.
// The codec doesn't matter; FLAC stands in for the Opus and Vorbis books
// (#3175) because every FFmpeg build can encode it.
func TestAudioOnlyRemuxStreamsShortFragments(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}

	sourcePath := filepath.Join(t.TempDir(), "book.flac")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	encode := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "12", "-c:a", "flac", "-y", sourcePath,
	)
	if output, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("generate audio fixture: %v\n%s", err, output)
	}

	args := buildRemuxArgsWithLeadingPictureDropV3(sourcePath, "mp4", 0, true, -1, 0, false, true, 1, 2, 128, false, "", nil)
	var stderr bytes.Buffer
	remux := exec.CommandContext(ctx, ffmpegPath, args...)
	remux.Stderr = &stderr
	output, err := remux.Output()
	if err != nil {
		t.Fatalf("ffmpeg remux failed: %v\n%s", err, stderr.Bytes())
	}
	boxes, err := fmp4Boxes(output)
	if err != nil {
		t.Fatalf("parse remux output: %v", err)
	}
	fragments := 0
	for _, box := range boxes {
		if box.typ == "moof" {
			fragments++
		}
	}
	// 12 seconds in fragments of about 2 seconds.
	if fragments < 5 {
		t.Fatalf("audio-only remux wrote %d fragments for 12 seconds of audio, want one about every 2 seconds", fragments)
	}
}
