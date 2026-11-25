package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/gofont/gosmallcaps"
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
	fonts, err := ExtractAttachedSubtitleFonts(t.Context(), "input.mkv", ffmpeg, 0, nil)
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
	_, err := ExtractAttachedSubtitleFonts(ctx, "fixture.mkv", ffmpeg, 0, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("extraction did not stop at deadline")
	}
}

// Fansub releases attach far more fonts than the bundle holds, often in
// alphabetical order, so the fonts a script names can come after the limit.
// The bundle must hold the attachments whose family the script names first,
// found by the names inside each font rather than its file name, then fill
// the rest in container order.
func TestExtractSubtitleFontBundleKeepsScriptFontsFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	total := maxSubtitleFontAttachments + 8
	fontData := func(i int) []byte {
		switch i {
		case total - 3:
			return gomono.TTF
		case total - 1:
			return gosmallcaps.TTF
		default:
			return goregular.TTF
		}
	}
	streams := make([]string, 0, total)
	for i := range total {
		stream := i + 2
		data := fontData(i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("font-%d", stream)), data, 0o600); err != nil {
			t.Fatal(err)
		}
		streams = append(streams, fmt.Sprintf(`{"index":%d,"extradata_size":%d,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"f%02d.ttf"}}`, stream, len(data), i))
	}
	writeExecutable(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nprintf '%s' '{\"streams\":["+strings.Join(streams, ",")+"]}'\n")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpeg, `#!/bin/sh
for arg do
 case "$arg" in
  -dump_attachment:*) cat "`+dir+`/font-${arg#-dump_attachment:}" ;;
 esac
done
`)
	scriptFonts := func(context.Context) []string { return []string{"go mono", "Go Smallcaps"} }
	fonts, err := extractSubtitleFontBundle(t.Context(), "input.mkv", ffmpeg, scriptFonts)
	if err != nil {
		t.Fatalf("extractSubtitleFontBundle returned error: %v", err)
	}
	if len(fonts) != maxSubtitleFontAttachments {
		t.Fatalf("bundle holds %d fonts, want %d", len(fonts), maxSubtitleFontAttachments)
	}
	var names []string
	for i, font := range fonts {
		names = append(names, font.Name)
		if i > 0 && font.StreamIndex < fonts[i-1].StreamIndex {
			t.Fatalf("bundle is not in container order: %v", names)
		}
		if !bytes.Equal(font.Data, fontData(font.StreamIndex-2)) {
			t.Fatalf("font %s carries another attachment's data", font.Name)
		}
	}
	for _, want := range []string{fmt.Sprintf("f%02d.ttf", total-3), fmt.Sprintf("f%02d.ttf", total-1), "f00.ttf", "f29.ttf"} {
		if !slices.Contains(names, want) {
			t.Errorf("bundle %v leaves out %s", names, want)
		}
	}
}

func TestASSScriptFontNames(t *testing.T) {
	header := "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n" +
		"Format: Name, Fontname, Fontsize, PrimaryColour\n" +
		"Style: Default,Adobe Arabic,40,&H00FFFFFF\n" +
		"Style: Vertical,@FOT-TelopMin Pro D,40,&H00FFFFFF\n" +
		"Style: Dup, adobe arabic ,40,&H00FFFFFF\n\n" +
		"[V4 Styles]\nFormat: Fontname, Name\nStyle: Times New Roman,Old\n\n" +
		"[Events]\nFormat: Layer, Start, End, Style, Text\n"
	events := [][]byte{
		[]byte(`1,0,Default,,0,0,0,,{\fnEl Messiri\b1}text{\fn}reset`),
		[]byte(`2,0,Sign,,0,0,0,,{\pos(1,2)\fn@Bahij Nassim}a{\fs20\fnComic Sans MS}b`),
		[]byte(`3,0,Default,,0,0,0,,plain {not a tag fnX} \fnLiteral`),
		[]byte(`4,0,Default,,0,0,0,,{\fn(Trebuchet MS)}paren`),
	}
	got := assScriptFontNames(append([][]byte{[]byte(header)}, events...)...)
	want := []string{"adobe arabic", "fot-telopmin pro d", "times new roman", "el messiri", "bahij nassim", "comic sans ms", "trebuchet ms"}
	if !slices.Equal(got, want) {
		t.Fatalf("assScriptFontNames = %q, want %q", got, want)
	}
}

// The bundle request names the selected track by its ordinal among the
// file's subtitle streams: each track's styles come from its own header in
// the source's index, and its \fn overrides once the subtitle cache holds the
// extracted track.
//
//nolint:misspell // ASS uses the literal Dialogue record type.
func TestSubtitleScriptFontsReadsSelectedTrack(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to build the Matroska fixture")
	}
	dir := t.TempDir()
	script := func(font, override string) string {
		return "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n" +
			"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
			"Style: Default," + font + ",20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n" +
			"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
			"Dialogue: 0,0:00:00.00,0:00:00.10,Default,,0,0,0,,{\\fn" + override + "}Hello\n"
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=size=16x16:duration=0.1"}
	for i, s := range []string{script("First Style", "First Override"), script("Second Style", "Second Override")} {
		path := filepath.Join(dir, fmt.Sprintf("%d.ass", i))
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-i", path)
	}
	media := filepath.Join(dir, "tracks.mkv")
	args = append(args, "-map", "0:v", "-map", "1:s", "-map", "2:s", "-c:v", "ffv1", "-c:s", "ass", media)
	if out, err := exec.CommandContext(t.Context(), ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}

	if got, want := subtitleScriptFonts(t.Context(), media, 1, nil), []string{"second style"}; !slices.Equal(got, want) {
		t.Fatalf("track 1 without cache = %q, want %q", got, want)
	}
	cache := NewSubtitleCache(func() string { return filepath.Join(dir, "cache") })
	if _, err := cache.ExtractText(t.Context(), media, 1, subtitleFormatASS, func(context.Context) ([]byte, error) {
		return []byte(script("Second Style", "Second Override")), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := subtitleScriptFonts(t.Context(), media, 1, cache), []string{"second style", "second override"}; !slices.Equal(got, want) {
		t.Fatalf("track 1 with cache = %q, want %q", got, want)
	}
	if got, want := subtitleScriptFonts(t.Context(), media, 0, cache), []string{"first style"}; !slices.Equal(got, want) {
		t.Fatalf("track 0 = %q, want %q", got, want)
	}
}

// A crafted collection gives each directory its own header over one shared
// name table, each seeing a slightly different slice of it so no two look
// alike, and fills the table with records that repeat one name. Without a
// budget on the records examined it decodes the table 256 times, about 11 s
// of CPU per attachment, past the extraction deadline.
func TestFontNamesBoundsCraftedCollection(t *testing.T) {
	const records = 65535
	table := binary.BigEndian.AppendUint16(nil, 0)
	table = binary.BigEndian.AppendUint16(table, records)
	// Storage at 0: every record names the same 256 bytes.
	table = binary.BigEndian.AppendUint16(table, 0)
	for range records {
		for _, v := range []uint16{1, 0, 0, fontNameIDFamily, maxFontNameBytes, 0} {
			table = binary.BigEndian.AppendUint16(table, v)
		}
	}

	const dirs = 256
	headersAt := 12 + 4*dirs
	tableAt := headersAt + 28*dirs
	data := append([]byte("ttcf"), 0, 1, 0, 0)
	data = binary.BigEndian.AppendUint32(data, dirs)
	for i := range dirs {
		data = binary.BigEndian.AppendUint32(data, uint32(headersAt+28*i))
	}
	for i := range dirs {
		data = append(data, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0)
		data = append(data, "name"...)
		data = binary.BigEndian.AppendUint32(data, 0)
		data = binary.BigEndian.AppendUint32(data, uint32(tableAt))
		data = binary.BigEndian.AppendUint32(data, uint32(len(table)-i))
	}
	data = append(data, table...)

	start := time.Now()
	names := fontNames(data)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fontNames took %s on a crafted collection", elapsed)
	}
	if len(names) != 1 {
		t.Fatalf("fontNames kept %d names, want the one repeated name", len(names))
	}
}

// A crafted track can name a new font in every event. The families read from
// it must stay bounded: a bundle holds 32 fonts, so a few hundred families is
// ample, and comparing each attachment's names against every family of an
// unbounded list would keep the request busy long past its deadline.
//
//nolint:misspell // ASS uses the literal Dialogue record type.
func TestASSScriptFontNamesBoundsDistinctNames(t *testing.T) {
	var script strings.Builder
	script.WriteString("[Events]\n")
	for i := range 20000 {
		fmt.Fprintf(&script, "Dialogue: 0,0:00:00.00,0:00:01.00,Default,,0,0,0,,{\\fnF%d}x\n", i)
	}
	names := assScriptFontNames([]byte(script.String()))
	if len(names) == 0 || names[0] != "f0" || len(names) > 256 {
		t.Fatalf("assScriptFontNames kept %d families, want the first few hundred", len(names))
	}
}

// Reading every attachment's names can outlast the request on slow storage.
// The bundle must then still serve the fonts that fit, in container order, as
// it did before it read names, rather than fail.
func TestExtractSubtitleFontBundleKeepsContainerOrderWhenNamesAreSlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	total := maxSubtitleFontAttachments + 8
	streams := make([]string, 0, total)
	for i := range total {
		streams = append(streams, fmt.Sprintf(`{"index":%d,"extradata_size":1,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"f%02d.ttf"}}`, i+2, i))
	}
	writeExecutable(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nprintf '%s' '{\"streams\":["+strings.Join(streams, ",")+"]}'\n")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	// Reading names dumps every attachment and stalls; the bundle's own dump
	// of the fonts that fit answers at once.
	writeExecutable(t, ffmpeg, fmt.Sprintf(`#!/bin/sh
n=0
for arg do
 case "$arg" in -dump_attachment:*) n=$((n+1)) ;; esac
done
if [ "$n" -gt %d ]; then exec sleep 60; fi
for arg do
 case "$arg" in -dump_attachment:*) printf f ;; esac
done
`, maxSubtitleFontAttachments))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	fonts, err := extractSubtitleFontBundle(ctx, "input.mkv", ffmpeg, func(context.Context) []string { return []string{"go mono"} })
	if err != nil {
		t.Fatalf("extractSubtitleFontBundle returned error: %v", err)
	}
	if len(fonts) != maxSubtitleFontAttachments || fonts[0].Name != "f00.ttf" || fonts[len(fonts)-1].Name != "f31.ttf" {
		t.Fatalf("bundle holds %d fonts, want the first %d in container order", len(fonts), maxSubtitleFontAttachments)
	}
}
