package playback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	codecs := []string{"aac", "eac3", "ac3", "truehd", "dts", "opus", "flac", ""}
	containers := []string{"ts", "m2ts", "mkv", "mp4", "webm", "virtual", ""}
	for _, codec := range codecs {
		for _, container := range containers {
			name := "codec=" + codec + "/container=" + container
			t.Run(name, func(t *testing.T) {
				// AAC is ADTS-framed only in MPEG-TS/raw ADTS inputs, and a row
				// that names no container keeps the historical answer. Every
				// other codec must never see the filter, whatever the container.
				want := ""
				if codec == "aac" && (container == "ts" || container == "m2ts" || container == "") {
					want = "aac_adtstoasc"
				}
				args := buildRemuxArgsWithLeadingPictureDropV3("/x.mkv", "mp4", 0, false, -1, 0, false, false, 0, 0, 0, false, CopiedAudioADTSCodec(codec, container))
				if got := remuxAudioBitstreamFilterArg(args); got != want {
					t.Fatalf("-bsf:a = %q, want %q (args=%s)", got, want, strings.Join(args, " "))
				}
			})
		}
	}
}

// A re-encoded track is never ADTS input, so even an AAC target keeps the
// plain encode path.
func TestBuildRemuxArgsNeverFiltersReEncodedAudio(t *testing.T) {
	args := buildRemuxArgsWithLeadingPictureDropV3("/x.ts", "mp4", 0, true, -1, 0, false, false, 0, 0, 0, false, CopiedAudioADTSCodec("aac", "ts"))
	if got := remuxAudioBitstreamFilterArg(args); got != "" {
		t.Fatalf("-bsf:a = %q on a re-encoded track, want none (args=%s)", got, strings.Join(args, " "))
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

// A copied E-AC-3 track needs no audio bitstream filter, and FFmpeg's
// aac_adtstoasc refuses every codec but AAC ("Codec 'eac3' ... is not supported
// by the bitstream filter"). Production reached exactly that with a remux of a
// large MKV whose copy codec was labeled AAC: the remux produced no output, the
// stream answered 503, and the client was told the file could not be converted.
// The E-AC-3 copy must produce output with its audio intact.
func TestRemuxCopiesEAC3IntoFragmentedMP4(t *testing.T) {
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
	sourcePath := filepath.Join(dir, "source.mkv")
	encodeCtx, cancelEncode := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelEncode()
	encode := exec.CommandContext(encodeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "6",
		"-c:v", "mpeg4", "-g", "25",
		"-c:a", "eac3", "-ac", "2",
		"-y", sourcePath,
	)
	if output, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("generate E-AC-3 fixture: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := buildRemuxArgsWithLeadingPictureDropV3(sourcePath, "mp4", 0, false, -1, 0, false, false, 0, 0, 0, false, CopiedAudioADTSCodec("eac3", "mkv"))
	if got := remuxAudioBitstreamFilterArg(args); got != "" {
		t.Fatalf("-bsf:a = %q on a copied E-AC-3 track, want none (args=%s)", got, strings.Join(args, " "))
	}
	outputPath := filepath.Join(dir, "out.mp4")
	args[len(args)-1] = outputPath
	if output, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg remux failed: %v\n%s", err, output)
	}
	probe, err := exec.CommandContext(ctx, ffprobePath, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name:format=duration", "-of", "default=noprint_wrappers=1", outputPath).Output()
	if err != nil {
		t.Fatalf("probe remux output: %v", err)
	}
	if !strings.Contains(string(probe), "codec_type=audio") || !strings.Contains(string(probe), "codec_name=eac3") {
		t.Fatalf("remux output lost its E-AC-3 audio:\n%s", probe)
	}
}

// The exact failure this guards: the AAC-only ADTS filter applied to a copied
// E-AC-3 track. FFmpeg rejects the stream, and that rejection is this server's
// recipe fault, not a verdict about the release. A caller that treats it as the
// source failing marks a healthy candidate failed and rotates, and the client
// ends up told its file cannot be converted.
func TestRemuxRejectsAACFilterOnEAC3AsARecipeFault(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.mkv")
	encodeCtx, cancelEncode := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelEncode()
	if output, err := exec.CommandContext(encodeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "4", "-c:v", "mpeg4", "-g", "25", "-c:a", "eac3", "-ac", "2",
		"-y", sourcePath,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate E-AC-3 fixture: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stream/x", nil).WithContext(ctx)
	err = ServeRemuxWithOptions(rec, req, sourcePath, "mp4", 0, false, -1, 0, RemuxServeOptions{
		FFmpegPath:      ffmpegPath,
		CopyAudioCodec:  CopiedAudioADTSCodec("eac3", "mkv"), // "" — the plan's real answer
		DeferStartError: true,
	})
	if err != nil {
		t.Fatalf("the E-AC-3 copy failed with the plan's own filter choice: %v", err)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the E-AC-3 copy produced no output")
	}

	// Forcing the AAC-only filter (the pre-fix behavior) is classified as a
	// recipe fault, and nothing is committed by the transport.
	rec = httptest.NewRecorder()
	err = ServeRemuxWithOptions(rec, req, sourcePath, "mp4", 0, false, -1, 0, RemuxServeOptions{
		FFmpegPath:      ffmpegPath,
		CopyAudioCodec:  "aac",
		DeferStartError: true,
	})
	if !errors.Is(err, ErrRemuxRecipeFault) {
		t.Fatalf("forced ADTS filter on E-AC-3 = %v, want ErrRemuxRecipeFault", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("recipe fault committed %d body bytes; the caller must own the status", rec.Body.Len())
	}
}
