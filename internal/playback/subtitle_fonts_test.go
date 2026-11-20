package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeFFmpegDumping returns a shell script that mimics ffmpeg's
// -dump_attachment behavior: it writes one payload to stdout for each
// -dump_attachment:* flag. This exercises the single-invocation extractor
// without a real ffmpeg. The shebang must stay on the first line.
func fakeFFmpegDumping(payload string) string {
	return "#!/bin/sh\nPAYLOAD='" + payload + `'
prev=""
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*) printf '%s' "$PAYLOAD" ;;
  esac
  prev="$a"
done
`
}

// TestDumpFontAttachmentsArgvOrder pins the ffmpeg argument contract: every
// -dump_attachment flag must precede -i (they are per-input options for the
// following input), and the -map 0:t? / -c copy stream-copy flags must be
// present (without them ffmpeg decodes the whole video). A fake ffmpeg records
// its argv so a malformed invocation can't pass silently.
func TestDumpFontAttachmentsArgvOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+argvFile+`'
prev=""
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*) printf 'x' ;;
  esac
  prev="$a"
done
`)

	if _, err := dumpFontAttachments(context.Background(), "input.mkv", ffmpegPath,
		[]attachmentProbeStream{{Index: 2, ExtraDataSize: 1}, {Index: 5, ExtraDataSize: 1}}, 0, maxSubtitleFontBytes); err != nil {
		t.Fatalf("dumpFontAttachments returned error: %v", err)
	}

	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	inputIdx, mapIdx := -1, -1
	lastDumpIdx := -1
	for i, a := range argv {
		switch {
		case a == "-i":
			inputIdx = i
		case a == "-map":
			mapIdx = i
		case strings.HasPrefix(a, "-dump_attachment:"):
			lastDumpIdx = i
		}
	}
	if inputIdx < 0 {
		t.Fatalf("argv missing -i: %v", argv)
	}
	if lastDumpIdx < 0 || lastDumpIdx > inputIdx {
		t.Fatalf("dump_attachment flags must precede -i; argv=%v", argv)
	}
	// One video stream limited to no frames ends the run at the header;
	// mapping the attachments alone demuxes the whole file.
	if mapIdx < 0 || argv[mapIdx+1] != "0:0" || !containsSeq(argv, "-frames:v", "0") {
		t.Fatalf("argv missing -map 0:0 -frames:v 0: %v", argv)
	}
	if !containsSeq(argv, "-c", "copy") {
		t.Fatalf("argv missing -c copy: %v", argv)
	}
}

// A source without video still gives up its fonts: the header-only dump maps a
// video stream, so one without keeps the attachment-only output.
func TestExtractAttachedSubtitleFontsFromAudioOnlySource(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	font := testFontFile(t)
	src := filepath.Join(t.TempDir(), "lyrics.mka")
	if out, err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "anullsrc=d=1", "-c:a", "aac",
		"-attach", font, "-metadata:s:t", "mimetype=application/x-truetype-font", src).CombinedOutput(); err != nil {
		t.Fatalf("mux fixture: %v\n%s", err, out)
	}
	want, err := os.ReadFile(font)
	if err != nil {
		t.Fatal(err)
	}
	fonts, err := ExtractAttachedSubtitleFonts(t.Context(), src, ffmpeg)
	if err != nil || len(fonts) != 1 || !bytes.Equal(fonts[0].Data, want) {
		t.Fatalf("ExtractAttachedSubtitleFonts() = %d fonts, %v; want the attached font", len(fonts), err)
	}
}

func containsSeq(argv []string, a, b string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == a && argv[i+1] == b {
			return true
		}
	}
	return false
}

func TestExtractAttachedSubtitleFontsSingleInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
cat <<'JSON'
{"streams":[{"index":2,"extradata_size":8,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"MyFont.ttf","mimetype":"font/ttf"}},{"index":3,"extradata_size":8,"codec_name":"otf","codec_type":"attachment","tags":{"filename":"Other.otf","mimetype":"font/otf"}}]}
JSON
`)
	invocations := filepath.Join(dir, "invocations")
	t.Setenv("FONT_EXTRACTION_INVOCATIONS", invocations)
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nprintf 'run\\n' >> \"$FONT_EXTRACTION_INVOCATIONS\"\n"+strings.TrimPrefix(fakeFFmpegDumping("fontdata"), "#!/bin/sh\n"))

	fonts, err := ExtractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpegPath)
	if err != nil {
		t.Fatalf("ExtractAttachedSubtitleFonts returned error: %v", err)
	}
	started, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatalf("read invocation count: %v", err)
	}
	if string(started) != "run\n" {
		t.Fatalf("font extraction invocations = %q, want one", started)
	}
	if len(fonts) != 2 {
		t.Fatalf("font count = %d, want 2", len(fonts))
	}
	if fonts[0].Name != "MyFont.ttf" || fonts[1].Name != "Other.otf" {
		t.Fatalf("font names = %q/%q, want MyFont.ttf/Other.otf", fonts[0].Name, fonts[1].Name)
	}
	if string(fonts[0].Data) != "fontdata" {
		t.Fatalf("font data = %q, want fontdata", string(fonts[0].Data))
	}
}

func TestExtractAttachedSubtitleFontDumpsOnlyRequestedStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
printf '%s' '{"streams":[{"index":2,"extradata_size":13,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"first.ttf"}},{"index":7,"extradata_size":13,"codec_name":"otf","codec_type":"attachment"}]}'
`)
	writeExecutable(t, ffmpeg, `#!/bin/sh
for arg do
 case "$arg" in -dump_attachment:7) ;; -dump_attachment:*) exit 42;; esac
done
`+strings.TrimPrefix(fakeFFmpegDumping("selected-font"), "#!/bin/sh\n"))
	font, err := ExtractAttachedSubtitleFont(t.Context(), "input.mkv", ffmpeg, 7)
	if err != nil || font == nil || font.StreamIndex != 7 || font.Name != "attachment-1.otf" || string(font.Data) != "selected-font" {
		t.Fatalf("selected font=%+v err=%v", font, err)
	}
	metadata, err := ListAttachedSubtitleFonts(t.Context(), "input.mkv", ffmpeg)
	if err != nil || len(metadata) != 2 || metadata[1].FileName != font.Name {
		t.Fatalf("selected fallback name differs from discovery: %+v %v", metadata, err)
	}
	writeExecutable(t, ffmpeg, "#!/bin/sh\nexit 42\n")
	font, err = ExtractAttachedSubtitleFont(t.Context(), "input.mkv", ffmpeg, 99)
	if err != nil || font != nil {
		t.Fatalf("unknown stream started extraction: %+v %v", font, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExtractAttachedSubtitleFont(ctx, "input.mkv", ffmpeg, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled single-font extraction: %v", err)
	}
}

func TestUnsafeFontAttachmentNameAndMIMEAreReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
printf '%s' '{"streams":[{"index":4,"extradata_size":37,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"payload.html","mimetype":"text/html"}}]}'
`)
	writeExecutable(t, ffmpeg, fakeFFmpegDumping("<script>untrusted attachment</script>"))
	metadata, err := ListAttachedSubtitleFonts(t.Context(), "input.mkv", ffmpeg)
	if err != nil || len(metadata) != 1 || metadata[0].FileName != "attachment-0.ttf" || metadata[0].MimeType != "font/ttf" {
		t.Fatalf("unsafe discovery metadata: %+v %v", metadata, err)
	}
	font, err := ExtractAttachedSubtitleFont(t.Context(), "input.mkv", ffmpeg, 4)
	if err != nil || font == nil || font.Name != metadata[0].FileName || SubtitleFontMIMEType(font.Name) != "font/ttf" {
		t.Fatalf("unsafe single-font metadata: %+v %v", font, err)
	}
	for _, name := range []string{"payload.html", "payload.svg", "payload.js", "payload.font"} {
		if SubtitleFontMIMEType(name) != "application/octet-stream" {
			t.Fatalf("unsafe MIME for %s", name)
		}
	}
}

// Missing bytes cannot be assigned to the next font in a concatenated bundle.
func TestDumpFontAttachmentsRejectsMissingData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	// Only emits the first attachment, making the probed framing inconsistent.
	writeExecutable(t, ffmpegPath, `#!/bin/sh
prev=""
first=1
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*)
      if [ "$first" = "1" ]; then printf 'fontdata'; first=0; fi ;;
  esac
  prev="$a"
done
`)

	fonts, err := dumpFontAttachments(context.Background(), "input.mkv", ffmpegPath,
		[]attachmentProbeStream{{Index: 2, ExtraDataSize: 8}, {Index: 3, ExtraDataSize: 8}}, 0, maxSubtitleFontBytes)
	if err == nil || len(fonts) != 0 {
		t.Fatalf("missing attachment data accepted: %d fonts, %v", len(fonts), err)
	}
}

func TestDumpFontAttachmentsBoundedPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	writeExecutable(t, ffmpeg, fakeFFmpegDumping("1234"))
	streams := []attachmentProbeStream{{Index: 2, ExtraDataSize: 4}, {Index: 7, ExtraDataSize: 4}}
	fonts, err := dumpFontAttachments(t.Context(), "input.mkv", ffmpeg, streams, 0, 8)
	if err != nil || len(fonts) != 2 || string(fonts[0].Data) != "1234" || string(fonts[1].Data) != "1234" {
		t.Fatalf("exact bound: %+v %v", fonts, err)
	}
	for _, size := range []int64{-1, 0, 9} {
		if _, err := dumpFontAttachments(t.Context(), "input.mkv", "missing-ffmpeg", []attachmentProbeStream{{Index: 2, ExtraDataSize: size}}, 0, 8); err == nil || strings.Contains(err.Error(), "start attachment") {
			t.Fatalf("invalid size %d reached FFmpeg: %v", size, err)
		}
	}
	writeExecutable(t, ffmpeg, "#!/bin/sh\nwhile :; do printf '1234567890'; done\n")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := dumpFontAttachments(ctx, "input.mkv", ffmpeg, streams, 0, 8); err == nil || !strings.Contains(err.Error(), "exceeds probed size") {
		t.Fatalf("fast writer overflow: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("overflow relied on deadline instead of terminating writer")
	}
}

func TestFFprobePathFromFFmpegRewritesOnlyBasename(t *testing.T) {
	got := ffprobePathFromFFmpeg(filepath.Join("tmp", "ffmpeg-tools", "ffmpeg"))
	want := filepath.Join("tmp", "ffmpeg-tools", "ffprobe")
	if got != want {
		t.Fatalf("ffprobePathFromFFmpeg basename path = %q, want %q", got, want)
	}

	got = ffprobePathFromFFmpeg(filepath.Join("tmp", "ffmpeg-tools", "custom"))
	if got != "ffprobe" {
		t.Fatalf("ffprobePathFromFFmpeg custom basename = %q, want ffprobe", got)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFontExtractionHonorsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffprobe := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(ffprobe, []byte(`#!/bin/sh
printf '%s' '{"streams":[{"index":1,"extradata_size":1,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"font.ttf"}}]}'
`), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ExtractAttachedSubtitleFonts(ctx, "fixture.mkv", ffmpeg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("extraction did not stop at deadline")
	}
}

// Real releases can pack more than the old 32 MiB of fonts: the bundle failed
// hard and the client stayed on fallback fonts for every window. A 40 MiB pack
// (above the old ceiling, below the current one) must extract in one bounded
// invocation; anything past the ceiling still fails closed for the caller's
// pending/backoff path.
func TestExtractAttachedSubtitleFontsSupportsPacksAboveOldCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	const packBytes = 40 << 20
	if maxSubtitleFontBytes <= 32<<20 || maxSubtitleFontBytes < packBytes {
		t.Fatalf("font bundle cap = %d, want a bound above the old 32 MiB ceiling and at least %d", maxSubtitleFontBytes, packBytes)
	}

	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
printf '%s' '{"streams":[{"index":2,"extradata_size":41943040,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"Pack.ttf"}}]}'
`)
	writeExecutable(t, ffmpeg, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    -dump_attachment:2) head -c 41943040 /dev/zero; exit 0 ;;
  esac
done
`)

	fonts, err := ExtractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpeg)
	if err != nil {
		t.Fatalf("40 MiB pack failed under the current cap: %v", err)
	}
	if len(fonts) != 1 || len(fonts[0].Data) != packBytes {
		t.Fatalf("extracted %d fonts, first %d bytes, want one %d-byte font", len(fonts), len(fonts[0].Data), packBytes)
	}

	if _, err := dumpFontAttachments(context.Background(), "input.mkv", ffmpeg,
		[]attachmentProbeStream{{Index: 2, ExtraDataSize: maxSubtitleFontBytes + 1}}, 0, maxSubtitleFontBytes); err == nil {
		t.Fatal("a pack past the hard ceiling must fail closed instead of buffering")
	}
}

// The style Fontname column is positional, so the parser must follow the
// section's Format record rather than assume a fixed offset; inline \fn
// overrides in event lines are references too.
//
//nolint:misspell // ASS uses the literal Dialogue record type.
func TestParseASSFontFamilies(t *testing.T) {
	script := `[Script Info]
Title: fixture
[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour
Style: Default,Open Sans,20,&H00FFFFFF
Style: Sign,Futura Medium,18,&H00FFFFFF
Style: Vertical,@Open Sans,18,&H00FFFFFF
[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.00,0:00:05.00,Default,,0,0,0,,{\fnSignFont\bord0}Hello
Dialogue: 0,0:00:06.00,0:00:09.00,Default,,0,0,0,,{\FNSecond Font}Bye
`
	got := parseASSFontFamilies([]byte(script))
	want := []string{"Open Sans", "Futura Medium", "SignFont", "Second Font"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("families = %q, want %q", got, want)
	}
}

// embeddedFontNames must read the family from an sfnt name table, including a
// font truncated to the streamed prefix.
func TestEmbeddedFontNamesReadsSFNTNameTable(t *testing.T) {
	font := buildTestFont("Subset Fixture")
	names := embeddedFontNames(font)
	if !containsFold(names, "Subset Fixture") {
		t.Fatalf("names = %q, want the embedded family", names)
	}
	if len(embeddedFontNames(font[:20])) != 0 {
		t.Fatal("a truncated name table must yield no names")
	}
}

// A pack past the ceiling is reduced to the fonts its ASS script references:
// the unreferenced attachment is drained instead of retained.
//
//nolint:misspell // ASS uses the literal Dialogue record type.
func TestExtractAttachedSubtitleFontsSelectsReferencedSubset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	fontA := buildTestFont("Futura")
	fontB := buildTestFont("Arial")
	fontAPath := filepath.Join(dir, "futura.ttf")
	fontBPath := filepath.Join(dir, "arial.ttf")
	if err := os.WriteFile(fontAPath, fontA, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fontBPath, fontB, 0o600); err != nil {
		t.Fatal(err)
	}

	script := []byte("[V4+ Styles]\nFormat: Name, Fontname, Fontsize\nStyle: Default,Futura,20\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:00.00,0:00:05.00,Default,{\\fnFutura}Hi\n")
	headerJSON := filepath.Join(dir, "header.json")
	packetsJSON := filepath.Join(dir, "packets.json")
	writeASSScriptProbes(t, headerJSON, packetsJSON, script, 4)
	writeSubsetFFprobe(t, filepath.Join(dir, "ffprobe"), headerJSON, packetsJSON, []attachmentProbeStream{
		{Index: 2, ExtraDataSize: int64(len(fontA)), CodecName: "ttf", CodecType: "attachment", Tags: map[string]string{"filename": "futura.ttf"}},
		{Index: 3, ExtraDataSize: int64(len(fontB)), CodecName: "ttf", CodecType: "attachment", Tags: map[string]string{"filename": "arial.ttf"}},
	})
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  case "$a" in
    -dump_attachment:2) cat '%s' ;;
    -dump_attachment:3) cat '%s' ;;
  esac
done
`, fontAPath, fontBPath))

	// One byte below the complete pack forces the script-driven subset.
	maxBytes := int64(len(fontA) + len(fontB) - 1)
	fonts, err := extractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpeg, nil, maxBytes)
	if err != nil {
		t.Fatalf("subset extraction: %v", err)
	}
	if len(fonts) != 1 || fonts[0].StreamIndex != 2 || string(fonts[0].Data) != string(fontA) {
		t.Fatalf("subset = %d fonts; want only the Futura attachment", len(fonts))
	}
}

// If even the referenced fonts exceed the retained ceiling, the extraction
// still fails closed so the caller keeps its pending/backoff behavior.
func TestExtractAttachedSubtitleFontsSubsetRespectsCeiling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	fontA := buildTestFont("Futura")
	fontB := buildTestFont("Arial")
	fontAPath := filepath.Join(dir, "futura.ttf")
	fontBPath := filepath.Join(dir, "arial.ttf")
	if err := os.WriteFile(fontAPath, fontA, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fontBPath, fontB, 0o600); err != nil {
		t.Fatal(err)
	}
	script := []byte("[V4+ Styles]\nFormat: Name, Fontname\nStyle: Default,Futura\n")
	headerJSON := filepath.Join(dir, "header.json")
	packetsJSON := filepath.Join(dir, "packets.json")
	writeASSScriptProbes(t, headerJSON, packetsJSON, script, 4)
	writeSubsetFFprobe(t, filepath.Join(dir, "ffprobe"), headerJSON, packetsJSON, []attachmentProbeStream{
		{Index: 2, ExtraDataSize: int64(len(fontA)), CodecName: "ttf", CodecType: "attachment", Tags: map[string]string{"filename": "futura.ttf"}},
		{Index: 3, ExtraDataSize: int64(len(fontB)), CodecName: "ttf", CodecType: "attachment", Tags: map[string]string{"filename": "arial.ttf"}},
	})
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  case "$a" in
    -dump_attachment:2) cat '%s' ;;
    -dump_attachment:3) cat '%s' ;;
  esac
done
`, fontAPath, fontBPath))

	maxBytes := int64(len(fontA) - 1)
	if _, err := extractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpeg, nil, maxBytes); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("subset past the ceiling: %v, want a size failure", err)
	} else if !errors.Is(err, ErrSubtitleFontSubsetUnavailable) {
		t.Fatalf("subset failure = %v, want ErrSubtitleFontSubsetUnavailable so the client sees a system-font fallback", err)
	}
}

// A complete pack at or below the ceiling keeps the historical path and never
// pays for a script probe.
func TestExtractAttachedSubtitleFontsSkipsScriptProbeBelowCeiling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	font := buildTestFont("Futura")
	probeLog := filepath.Join(dir, "script-probes.log")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  if [ "$a" = "-show_packets" ]; then echo probe >> '%s'; exit 1; fi
done
printf '%%s' '{"streams":[{"index":2,"extradata_size":%d,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"futura.ttf"}}]}'
`, probeLog, len(font)))
	fontPath := filepath.Join(dir, "futura.ttf")
	if err := os.WriteFile(fontPath, font, 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  case "$a" in
    -dump_attachment:*) cat '%s' ;;
  esac
done
`, fontPath))

	fonts, err := extractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpeg, nil, int64(len(font)))
	if err != nil || len(fonts) != 1 {
		t.Fatalf("whole pack below ceiling: %d fonts, %v", len(fonts), err)
	}
	if data, err := os.ReadFile(probeLog); err == nil && len(data) > 0 {
		t.Fatalf("script probe ran for a pack below the ceiling: %q", data)
	}
}

// buildTestFont returns a minimal sfnt carrying one UTF-16BE family name record,
// which is all embedded name-table parsing needs.
func buildTestFont(family string) []byte {
	utf16 := make([]byte, 0, len(family)*2)
	for _, r := range family {
		var pair [2]byte
		binary.BigEndian.PutUint16(pair[:], uint16(r))
		utf16 = append(utf16, pair[:]...)
	}
	name := make([]byte, 18, 18+len(utf16))
	binary.BigEndian.PutUint16(name[0:2], 0)  // format
	binary.BigEndian.PutUint16(name[2:4], 1)  // count
	binary.BigEndian.PutUint16(name[4:6], 18) // string offset
	binary.BigEndian.PutUint16(name[6:8], 3)  // platform: Windows
	binary.BigEndian.PutUint16(name[8:10], 1) // encoding: Unicode BMP
	binary.BigEndian.PutUint16(name[10:12], 0x409)
	binary.BigEndian.PutUint16(name[12:14], 1) // name ID: family
	binary.BigEndian.PutUint16(name[14:16], uint16(len(utf16)))
	binary.BigEndian.PutUint16(name[16:18], 0)
	name = append(name, utf16...)

	font := make([]byte, 28, 28+len(name))
	binary.BigEndian.PutUint32(font[0:4], 0x00010000)
	binary.BigEndian.PutUint16(font[4:6], 1) // numTables
	copy(font[12:16], "name")
	binary.BigEndian.PutUint32(font[20:24], 28)
	binary.BigEndian.PutUint32(font[24:28], uint32(len(name)))
	return append(font, name...)
}

// writeASSScriptProbes writes the two ffprobe reports a subset extraction reads:
// the subtitle stream table (header extradata) and the event packets.
func writeASSScriptProbes(t *testing.T, headerPath, packetsPath string, script []byte, streamIndex int) {
	t.Helper()
	header := script
	events := []byte(nil)
	if index := bytes.Index(script, []byte("[Events]")); index >= 0 {
		header = script[:index]
		events = script[index:]
	}
	headerJSON := fmt.Sprintf(`{"streams":[{"index":%d,"codec_name":"ass","extradata":%q}]}`,
		streamIndex, ffprobeHexDump(header))
	var packets []string
	for _, line := range strings.Split(string(events), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		packets = append(packets, fmt.Sprintf(`{"stream_index":%d,"data":%q}`, streamIndex, ffprobeHexDump([]byte(line))))
	}
	packetsJSON := fmt.Sprintf(`{"packets":[%s]}`, strings.Join(packets, ","))
	if err := os.WriteFile(headerPath, []byte(headerJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packetsPath, []byte(packetsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ffprobeHexDump(data []byte) string {
	var b strings.Builder
	b.WriteByte('\n')
	for offset := 0; offset < len(data); offset += 16 {
		end := min(offset+16, len(data))
		fmt.Fprintf(&b, "%08x: ", offset)
		for i := offset; i < end; i += 2 {
			if i+1 < end {
				fmt.Fprintf(&b, "%02x%02x ", data[i], data[i+1])
			} else {
				fmt.Fprintf(&b, "%02x ", data[i])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// writeSubsetFFprobe fakes the three probes a subset extraction can issue:
// attachment metadata (`-select_streams t`), the subtitle stream table
// (`-select_streams s`), and one track's packets (`-show_packets`).
func writeSubsetFFprobe(t *testing.T, path, headerJSON, packetsJSON string, streams []attachmentProbeStream) {
	t.Helper()
	probe := attachmentProbeOutput{Streams: streams}
	raw, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, path, fmt.Sprintf(`#!/bin/sh
mode=""
packets=0
prev=""
for a in "$@"; do
  if [ "$a" = "-show_packets" ]; then packets=1; fi
  if [ "$prev" = "-select_streams" ]; then mode="$a"; fi
  prev="$a"
done
if [ "$packets" = "1" ]; then cat '%s'; exit 0; fi
if [ "$mode" = "s" ]; then cat '%s'; exit 0; fi
cat <<'JSON'
%s
JSON
`, packetsJSON, headerJSON, raw))
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
