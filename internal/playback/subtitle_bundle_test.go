package playback

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A bundle carries the selected track as an ASS script, with its container
// timestamps, and every font attachment under a generated name: the
// attachment's own filename, which the source chooses, never becomes a path.
func TestWriteSubtitleBundleWithRealFFmpeg(t *testing.T) {
	mkv, font := subtitleFixture(t, fixtureScriptHeader+
		"Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,Hello: relayed\nDialogue: 0,0:00:02.20,0:00:03.80,Default,,0,0,0,,Second line\n") //nolint:misspell // ASS event keyword
	var bundle bytes.Buffer
	if err := writeSubtitleBundle(t.Context(), SubtitleBundleSource{Path: mkv, Track: 0, Codec: "ass"}, t.TempDir(), &bundle); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := unpackSubtitleBundle(&bundle, dir); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(dir, subtitleBundleScript))
	if err != nil || !strings.Contains(string(script), "Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,,Hello: relayed") { //nolint:misspell // ASS event keyword
		t.Fatalf("bundled script %q, %v", script, err)
	}
	fonts, err := os.ReadDir(filepath.Join(dir, subtitleBundleFontsDir))
	if err != nil || len(fonts) != 2 || fonts[0].Name() != "attachment-2.ttf" || fonts[1].Name() != "attachment-3.ttf" {
		t.Fatalf("bundled fonts %v, %v", fonts, err)
	}
	want, _ := os.ReadFile(font)
	if got, _ := os.ReadFile(filepath.Join(dir, subtitleBundleFontsDir, "attachment-2.ttf")); !bytes.Equal(got, want) {
		t.Fatal("the bundled font differs from the attachment")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x.ttf")); err == nil {
		t.Fatal("the attachment's own filename became a path")
	}
}

// A script past the old 8 MiB bound is bundled whole.
func TestWriteSubtitleBundleCarriesALargeScript(t *testing.T) {
	var events strings.Builder
	line := "Dialogue: 0,0:00:00.50,0:00:02.00,Default,,0,0,0,," + strings.Repeat("x", 200) + "\n" //nolint:misspell // ASS event keyword
	for events.Len() < 9<<20 {
		events.WriteString(line)
	}
	mkv, _ := subtitleFixture(t, fixtureScriptHeader+events.String())
	var bundle bytes.Buffer
	if err := writeSubtitleBundle(t.Context(), SubtitleBundleSource{Path: mkv, Track: 0, Codec: "ass"}, t.TempDir(), &bundle); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := unpackSubtitleBundle(&bundle, dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, subtitleBundleScript)); err != nil || info.Size() < 8<<20 {
		t.Fatalf("bundled script: %v, %v", info, err)
	}
}

// A bundle carries the fonts libass would load, far past the bounds of the
// client font bundle: only an attachment without data, a repeated one, or
// one past the bundle's own bounds is left out.
func TestBundleFontStreamsKeepEveryRealFont(t *testing.T) {
	streams := []attachmentProbeStream{{Index: 1, ExtraDataSize: 0}, {Index: 2, ExtraDataSize: 20 << 20}, {Index: 2, ExtraDataSize: 20 << 20}}
	for i := range maxSubtitleFontAttachments + 13 {
		streams = append(streams, attachmentProbeStream{Index: 10 + i, ExtraDataSize: 1 << 20})
	}
	streams = append(streams, attachmentProbeStream{Index: 99, ExtraDataSize: maxBundleFontBytes})
	kept := bundleFontStreams(streams)
	if len(kept) != maxSubtitleFontAttachments+14 || kept[0].Index != 2 || kept[1].Index != 10 || kept[len(kept)-1].Index == 99 {
		t.Fatalf("kept %d fonts, first %+v", len(kept), kept[:2])
	}
}

// fakeExtractFFmpeg writes a stand-in ffmpeg that counts its runs in a file
// and exits with status, or writes an ASS script when status is 0.
func fakeExtractFFmpeg(t *testing.T, status int) (bin, runs string) {
	t.Helper()
	dir := t.TempDir()
	bin, runs = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho run >> \"" + runs + "\"\n"
	if status != 0 {
		script += "echo 'no such stream' >&2\nexit " + string(rune('0'+status)) + "\n"
	} else {
		script += "printf '[Script Info]\\n'\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// ffprobe beside it lists no attachments.
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\necho '{\"streams\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, runs
}

func countExtractRuns(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run")
}

// The cache builds a track's bundle once and serves it from then on.
func TestOpenSubtitleBundleBuildsOnce(t *testing.T) {
	transcodeDir := t.TempDir()
	cache := NewSubtitleCache(func() string { return transcodeDir })
	ffmpeg, runs := fakeExtractFFmpeg(t, 0)
	src := SubtitleBundleSource{Path: writeSource(t), Track: 1, Codec: "subrip", FFmpegPath: ffmpeg}
	for range 3 {
		f, err := cache.OpenSubtitleBundle(t.Context(), src)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := unpackSubtitleBundle(f, dir); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if n := countExtractRuns(t, runs); n != 1 {
		t.Fatalf("%d extractions for one track", n)
	}
	entries, _ := filepath.Glob(filepath.Join(transcodeDir, subtitleCacheDirName, "*"+subtitleBundleASSFormat))
	if len(entries) != 1 {
		t.Fatalf("cache entries %v", entries)
	}
}

// A request that runs out of time while the bundle builds is told it is
// pending; the build goes on and a later request gets the bundle.
func TestOpenSubtitleBundlePendsWhileBuilding(t *testing.T) {
	transcodeDir := t.TempDir()
	cache := NewSubtitleCache(func() string { return transcodeDir })
	ffmpeg, _ := fakeExtractFFmpeg(t, 0)
	gate := filepath.Join(t.TempDir(), "gate")
	script, _ := os.ReadFile(ffmpeg)
	slow := strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\nwhile [ ! -e '"+gate+"' ]; do sleep 0.01; done\n", 1)
	if err := os.WriteFile(ffmpeg, []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	src := SubtitleBundleSource{Path: writeSource(t), Track: 0, Codec: "ass", FFmpegPath: ffmpeg}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := cache.OpenSubtitleBundle(ctx, src); !errors.Is(err, ErrSubtitleBundlePending) {
		t.Fatalf("open while building = %v", err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := cache.OpenSubtitleBundle(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// A build that fails on the source's bytes is remembered: requests within
// its time get the failure without another whole-source demux.
func TestOpenSubtitleBundleRemembersAFailure(t *testing.T) {
	transcodeDir := t.TempDir()
	cache := NewSubtitleCache(func() string { return transcodeDir })
	ffmpeg, runs := fakeExtractFFmpeg(t, 1)
	src := SubtitleBundleSource{Path: writeSource(t), Track: 4, Codec: "ass", FFmpegPath: ffmpeg}
	for range 3 {
		if _, err := cache.OpenSubtitleBundle(t.Context(), src); !errors.Is(err, ErrSubtitleBundleFailed) || !strings.Contains(err.Error(), "no such stream") {
			t.Fatalf("open = %v", err)
		}
	}
	if n := countExtractRuns(t, runs); n != 1 {
		t.Fatalf("%d extractions after a failure", n)
	}
	cache.mu.Lock()
	for key, failure := range cache.bundleFailures {
		cache.bundleFailures[key] = subtitleBundleFailure{err: failure.err, at: failure.at.Add(-subtitleBundleFailureTTL)}
	}
	cache.mu.Unlock()
	if _, err := cache.OpenSubtitleBundle(t.Context(), src); !errors.Is(err, ErrSubtitleBundleFailed) {
		t.Fatalf("open after the failure's time = %v", err)
	}
	if n := countExtractRuns(t, runs); n != 2 {
		t.Fatalf("%d extractions after the failure's time", n)
	}
}

// rawBundle builds a tar from entries as given, malformed ones included.
func rawBundle(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, hdr := range entries {
		data := assScriptHeader + "\n"
		if hdr.Typeflag == tar.TypeReg && hdr.Size == 0 {
			hdr.Size = int64(len(data))
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size == int64(len(data)) {
			if _, err := io.WriteString(tw, data); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = tw.Flush()
	return buf.Bytes()
}

// A node unpacks only what a bundle holds: the script, and plain font files
// in the fonts directory, within the bundle's bounds.
func TestUnpackSubtitleBundleRefusesOtherEntries(t *testing.T) {
	script := tar.Header{Typeflag: tar.TypeReg, Name: subtitleBundleScript, Mode: 0o600}
	font := func(name string) tar.Header {
		return tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600}
	}
	for name, bundle := range map[string][]byte{
		"no script":        rawBundle(t, font("fonts/a.ttf")),
		"two scripts":      rawBundle(t, script, script),
		"escaping font":    rawBundle(t, script, font("fonts/../../x.ttf")),
		"absolute font":    rawBundle(t, script, font("/etc/x.ttf")),
		"nested font":      rawBundle(t, script, font("fonts/a/b.ttf")),
		"hidden font":      rawBundle(t, script, font("fonts/.ttf")),
		"not a font":       rawBundle(t, script, font("fonts/run.sh")),
		"other file":       rawBundle(t, script, font("notes.txt")),
		"symlink":          rawBundle(t, script, tar.Header{Typeflag: tar.TypeSymlink, Name: "fonts/a.ttf", Linkname: "/etc/passwd"}),
		"directory":        rawBundle(t, script, tar.Header{Typeflag: tar.TypeDir, Name: "fonts/sub/", Mode: 0o700}),
		"oversized script": rawBundle(t, tar.Header{Typeflag: tar.TypeReg, Name: subtitleBundleScript, Size: maxBundleScriptBytes + 1, Mode: 0o600}),
	} {
		dir := t.TempDir()
		if err := unpackSubtitleBundle(bytes.NewReader(bundle), dir); err == nil {
			t.Errorf("%s: unpacked", name)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), "x.ttf")); err == nil {
			t.Fatalf("%s: wrote outside the directory", name)
		}
	}
	if err := unpackSubtitleBundle(bytes.NewReader(rawBundle(t, script, font("fonts/a.ttf"))), t.TempDir()); err != nil {
		t.Fatalf("a valid bundle = %v", err)
	}
}
