package playback

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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
