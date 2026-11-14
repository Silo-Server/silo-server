package playback

import (
	"context"
	"os"
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
			args := buildRemuxArgsWithLeadingPictureDropV3("/x.ts", "mp4", 0, test.transcodeAudio, -1, 0, false, false, 0, 0, 0, false, test.codec, nil)
			if got := remuxAudioBitstreamFilterArg(args); got != test.want {
				t.Fatalf("-bsf:a = %q, want %q (args=%s)", got, test.want, strings.Join(args, " "))
			}
		})
	}
}

// The HLS muxer does not insert aac_adtstoasc for fMP4 segments either, so a
// copied ADTS track fails after init.mp4 (#2840). MPEG-TS segments carry ADTS
// as it is and must not get the filter.
func TestBuildFFmpegArgsConvertsADTSOnlyForCopiedAACInFMP4(t *testing.T) {
	for _, test := range []struct {
		name string
		opts TranscodeOpts
		want string
		fMP4 bool
	}{
		{name: "copied aac in fmp4", opts: TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: "copy", CopyAudioCodec: "aac"}, want: "aac_adtstoasc", fMP4: true},
		{name: "copied aac with copy in any case", opts: TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: " COPY ", CopyAudioCodec: "aac"}, want: "aac_adtstoasc", fMP4: true},
		{name: "copied aac under encoded hevc", opts: TranscodeOpts{TargetCodecVideo: "hevc", TargetCodecAudio: "copy", CopyAudioCodec: "aac", HWAccel: "none"}, want: "aac_adtstoasc", fMP4: true},
		{name: "copied aac in mpeg-ts", opts: TranscodeOpts{TargetCodecVideo: "copy", CopyVideoMPEGTS: true, TargetCodecAudio: "copy", CopyAudioCodec: "aac"}, want: ""},
		{name: "copied ac3 would be rejected by the filter", opts: TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: "copy", CopyAudioCodec: "ac3"}, want: "", fMP4: true},
		{name: "unknown codec from an older recipe", opts: TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: "copy"}, want: "", fMP4: true},
		{name: "encoded aac is never adts", opts: TranscodeOpts{TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyAudioCodec: "aac"}, want: "", fMP4: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := test.opts
			opts.InputPath, opts.OutputDir, opts.SourceVideoCodec, opts.AudioTrackIndex = "/x.ts", "/out", "h264", -1
			args := buildFFmpegArgs(opts)
			if got := remuxAudioBitstreamFilterArg(args); got != test.want {
				t.Fatalf("-bsf:a = %q, want %q (args=%s)", got, test.want, strings.Join(args, " "))
			}
			if got := HLSOutputContainer(opts) == OutputContainerFMP4; got != test.fMP4 {
				t.Fatalf("fMP4 segments = %v, want %v", got, test.fMP4)
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

// The issue's reproduction: an HLS copy of an MPEG-TS source with AAC audio
// must write every fMP4 segment, not stop after init.mp4.
func TestHLSCopiesADTSAACIntoFMP4Segments(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.ts")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	encode := exec.CommandContext(ctx, ffmpegPath,
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

	outputDir := filepath.Join(dir, "hls")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	args := buildFFmpegArgs(TranscodeOpts{InputPath: sourcePath, OutputDir: outputDir, TargetCodecVideo: "copy", TargetCodecAudio: "copy", CopyAudioCodec: "aac", SourceVideoCodec: "mpeg4", SegmentDuration: 2, AudioTrackIndex: -1})
	if output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg HLS copy failed: %v\n%s", err, output)
	}
	segments, err := filepath.Glob(filepath.Join(outputDir, "seg_*.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 3 {
		t.Fatalf("HLS copy wrote %d fMP4 segments, want at least 3: %v", len(segments), segments)
	}
}
