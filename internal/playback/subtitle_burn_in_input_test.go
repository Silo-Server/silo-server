package playback

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:misspell // ASS uses the literal Dialogue record type.
const burnInTestASS = `[Script Info]
ScriptType: v4.00+
PlayResX: 320
PlayResY: 240

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,DejaVu Sans,28,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,First line
Dialogue: 0,0:00:03.00,0:00:04.50,Default,,0,0,0,,{\an8}Second line
`

// burnInTestSource muxes a short video with an ASS track and fonts attached
// copies of a font into a Matroska file, as release groups do, starting at
// startOffset seconds.
func burnInTestSource(t *testing.T, ffmpeg string, startOffset float64, fonts int) string {
	t.Helper()
	font := testFontFile(t)
	dir := t.TempDir()
	ass := filepath.Join(dir, "subs.ass")
	if err := os.WriteFile(ass, []byte(burnInTestASS), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "source.mkv")
	args := []string{"-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=5", "-i", ass,
		"-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-c:s", "ass"}
	for range fonts {
		args = append(args, "-attach", font)
	}
	args = append(args, "-metadata:s:t", "mimetype=application/x-truetype-font")
	if startOffset != 0 {
		args = append(args, "-output_ts_offset", fmt.Sprint(startOffset))
	}
	if out, err := exec.Command(ffmpeg, append(args, src)...).CombinedOutput(); err != nil {
		t.Fatalf("mux fixture: %v\n%s", err, out)
	}
	return src
}

// testFontFile is a TrueType font on this host to attach to fixtures.
func testFontFile(t *testing.T) string {
	t.Helper()
	font := "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
	if _, err := os.Stat(font); err == nil {
		return font
	}
	if out, err := exec.Command("fc-match", "--format=%{file}", "sans:fontformat=TrueType").Output(); err == nil && strings.HasSuffix(string(out), ".ttf") {
		return string(out)
	}
	t.Skip("no TrueType font installed")
	return ""
}

func burnInTestOpts(t *testing.T, ffmpeg, src string) TranscodeOpts {
	return TranscodeOpts{
		InputPath:          src,
		OutputDir:          t.TempDir(),
		FFmpegPath:         ffmpeg,
		SubtitleBurnIn:     true,
		SubtitleTrackIndex: 0,
		SubtitleCodec:      "ass",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
	}
}

// Text burn-in hands libass the selected track on its own, with the source's
// fonts, instead of the media file, which it would read whole before the first
// frame. The staged track keeps the source's timestamps, so with -copyts libass
// draws the same frames from it as from the media file, after a seek too and on
// a source that doesn't start at zero.
func TestPrepareSubtitleFilterInputStagesTextTrack(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	// More fonts than the client font bundle takes: libass needs them all.
	for _, tc := range []struct {
		start float64
		fonts int
	}{{0, 1}, {5, 40}} {
		start := tc.start
		src := burnInTestSource(t, ffmpeg, start, tc.fonts)
		opts := burnInTestOpts(t, ffmpeg, src)
		// A restart prepares the input again over the first copy.
		for attempt := range 2 {
			if err := prepareSubtitleFilterInput(t.Context(), &opts); err != nil {
				t.Fatalf("start %v attempt %d: prepareSubtitleFilterInput() error = %v", start, attempt, err)
			}
		}
		if opts.subtitleFilterInputPath == "" || opts.subtitleFilterInputPath == filepath.Join(opts.OutputDir, subtitleFilterAliasName) {
			t.Fatalf("start %v: libass reads %q, the media file", start, opts.subtitleFilterInputPath)
		}
		if fonts, err := os.ReadDir(opts.subtitleFilterFontsDir); err != nil || len(fonts) != tc.fonts {
			t.Fatalf("start %v: staged %d fonts (%v), want the %d attached", start, len(fonts), err, tc.fonts)
		}

		fromSource := "subtitles=filename='" + escapeFilterPath(src) + "':si=0"
		fromStaged := textSubtitleFilter(opts)
		for _, seek := range []float64{0, 1.5, 3.5} {
			want := burnInTestFrame(t, ffmpeg, src, seek, fromSource)
			if got := burnInTestFrame(t, ffmpeg, src, seek, fromStaged); got != want {
				t.Errorf("start %v seek %v: staged track draws %s, source draws %s", start, seek, got, want)
			}
		}
		// The sampled frames carry subtitles, so the comparison means something.
		if burnInTestFrame(t, ffmpeg, src, 1.5, "null") == burnInTestFrame(t, ffmpeg, src, 1.5, fromStaged) {
			t.Fatalf("start %v: no subtitle drawn at 1.5 s", start)
		}
	}
}

// burnInTestFrame hashes the first frame from seek, filtered by vf, with the
// timestamps a burn-in transcode keeps (-copyts). FFmpeg counts -ss from the
// source's start time.
func burnInTestFrame(t *testing.T, ffmpeg, src string, seek float64, vf string) string {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-copyts", "-ss", fmt.Sprint(seek), "-i", src,
		"-map", "0:v:0", "-vf", vf, "-frames:v", "1", "-f", "framemd5", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("render %s at %v: %v\n%s", vf, seek, err, out)
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	last := string(lines[len(lines)-1])
	if strings.HasPrefix(last, "#") {
		t.Fatalf("render %s at %v: no frame", vf, seek)
	}
	fields := strings.Split(last, ",")
	return strings.TrimSpace(fields[1]) + "/" + strings.TrimSpace(fields[len(fields)-1])
}
