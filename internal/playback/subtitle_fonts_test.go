package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Fansub releases can attach more font data than the bundle limit. The bundle
// must skip a font that does not fit and fill its place with later fonts,
// rather than fail and leave none.
func TestExtractAttachedSubtitleFontsSkipsFontsOverByteLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	streams := []string{fmt.Sprintf(`{"index":2,"extradata_size":%d,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"huge.ttf"}}`, maxSubtitleFontBytes+1)}
	want := make([]string, 0, maxSubtitleFontAttachments)
	for i := 3; i < 3+maxSubtitleFontAttachments; i++ {
		streams = append(streams, fmt.Sprintf(`{"index":%d,"extradata_size":1,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"f%d.ttf"}}`, i, i))
		want = append(want, fmt.Sprintf("f%d.ttf:1", i))
	}
	writeExecutable(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nprintf '%s' '{\"streams\":["+strings.Join(streams, ",")+"]}'\n")
	writeExecutable(t, ffmpeg, `#!/bin/sh
for arg do
 case "$arg" in
  -dump_attachment:2) exit 42 ;;
  -dump_attachment:*) printf f ;;
 esac
done
`)
	fonts, err := ExtractAttachedSubtitleFonts(t.Context(), "input.mkv", ffmpeg)
	if err != nil {
		t.Fatalf("ExtractAttachedSubtitleFonts returned error: %v", err)
	}
	got := make([]string, 0, len(fonts))
	for _, font := range fonts {
		got = append(got, fmt.Sprintf("%s:%d", font.Name, len(font.Data)))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fonts = %v, want %v", got, want)
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
