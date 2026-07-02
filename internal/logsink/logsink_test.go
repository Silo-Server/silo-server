package logsink

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCategoryForComponent(t *testing.T) {
	cases := map[string]Category{
		"jellycompat":                  CategoryJellycompat,
		"ffmpeg":                       CategoryPlayback,
		"api":                          CategoryAPI,
		"admin":                        CategoryAPI,
		"scanner":                      CategoryScanner,
		"scan":                         CategoryScanner,
		"metadata":                     CategoryMetadata,
		"notifications":                CategoryNotifications,
		"notifications.webhooks.retry": CategoryNotifications, // longest-prefix fallback
		"NotIfIcAtIoNs":                CategoryNotifications, // case-insensitive
		"jellycompat autoscan":         CategoryJellycompat,   // multi-word: leading token wins
		"":                             CategoryApp,
		"something-unmapped":           CategoryApp,
		"unmapped detail here":         CategoryApp, // leading token also unmapped
	}
	for component, want := range cases {
		if got := CategoryForComponent(component); got != want {
			t.Errorf("CategoryForComponent(%q) = %q, want %q", component, got, want)
		}
	}
}

// TestRouterFileRouting is the "do logs actually land where I think" check: it
// drives real records through the handler and asserts the on-disk files.
func TestRouterFileRouting(t *testing.T) {
	dir := t.TempDir()
	combined := filepath.Join(dir, "silo.log")

	var console bytes.Buffer
	h, closer, err := New(Options{
		Stderr: &console,
		Format: "text",
		Level:  slog.LevelInfo,
		File:   combined,
		Split:  true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger := slog.New(h)

	// Static component attribute via With(...).
	logger.With("component", "jellycompat").Info("compat request served")
	// Message-prefix inference ("scanner:" -> scanner category).
	logger.Info("scanner: indexed library")
	// Static component mapping to playback.
	logger.With("component", "ffmpeg").Warn("transcode stalled")
	// Inline component attr on the log call itself (distinct code path from
	// With): must route to its category file.
	logger.Info("cache warmed", "component", "metadata")
	// Multi-word message prefix: InferComponent yields "jellycompat autoscan";
	// the leading-token fallback must still route it to jellycompat.
	logger.Info("jellycompat autoscan: listing libraries")
	// Inline component overrides a static one (mirrors opslog last-write-wins):
	// routes to scanner, not api.
	logger.With("component", "api").Info("done", "component", "scanner")
	// Unmapped -> app: combined + console only, no dedicated file.
	logger.Info("just some app noise")
	// Component nested in a group must NOT route (namespaced key) -> app.
	logger.WithGroup("grp").With("component", "jellycompat").Info("grouped noise")

	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "" // missing file reads as empty; assertions below handle it
		}
		return string(b)
	}

	combinedContent := read("silo.log")
	for _, msg := range []string{
		"compat request served", "scanner: indexed library", "transcode stalled",
		"cache warmed", "jellycompat autoscan: listing libraries", "done",
		"just some app noise", "grouped noise",
	} {
		if !contains(combinedContent, msg) {
			t.Errorf("combined log missing %q\n---\n%s", msg, combinedContent)
		}
	}

	// Console mirrors the combined stream.
	if !contains(console.String(), "compat request served") || !contains(console.String(), "just some app noise") {
		t.Errorf("console missing expected records:\n%s", console.String())
	}

	jelly := read("silo-jellycompat.log")
	if !contains(jelly, "compat request served") {
		t.Errorf("jellycompat file missing static-component record:\n%s", jelly)
	}
	if !contains(jelly, "jellycompat autoscan: listing libraries") {
		t.Errorf("jellycompat file missing multi-word-prefix record:\n%s", jelly)
	}
	if contains(jelly, "scanner: indexed library") || contains(jelly, "grouped noise") {
		t.Errorf("jellycompat file captured foreign records:\n%s", jelly)
	}

	scan := read("silo-scanner.log")
	if !contains(scan, "scanner: indexed library") {
		t.Errorf("scanner file missing prefix-inferred record:\n%s", scan)
	}
	// Inline component "scanner" overrode the static "api".
	if !contains(scan, "done") {
		t.Errorf("scanner file missing inline-overrides-static record:\n%s", scan)
	}
	if api := read("silo-api.log"); contains(api, "done") {
		t.Errorf("api file wrongly captured a record whose inline component was scanner:\n%s", api)
	}

	if play := read("silo-playback.log"); !contains(play, "transcode stalled") {
		t.Errorf("playback file missing its record:\n%s", play)
	}
	// Inline component attr on the log call routed to the metadata file.
	if meta := read("silo-metadata.log"); !contains(meta, "cache warmed") {
		t.Errorf("metadata file missing inline-component record:\n%s", meta)
	}

	// "app" is the default bucket and must never spawn a dedicated file.
	if _, err := os.Stat(filepath.Join(dir, "silo-app.log")); !os.IsNotExist(err) {
		t.Errorf("unexpected silo-app.log created (err=%v)", err)
	}
	// Grouped component=jellycompat must have fallen through to app (combined
	// only), never the jellycompat file (already checked above).
}

func TestNewNoFileIsConsoleOnly(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	h, closer, err := New(Options{Stderr: &console, Format: "text", Level: slog.LevelInfo})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closer.Close()

	slog.New(h).Info("hello")
	if !contains(console.String(), "hello") {
		t.Fatalf("console missing record: %q", console.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("expected no files created, got %d", len(entries))
	}
}

// TestRotation drives lumberjack past its size threshold and asserts a rotated
// backup is produced (rotation was previously untested).
func TestRotation(t *testing.T) {
	dir := t.TempDir()
	combined := filepath.Join(dir, "silo.log")
	h, closer, err := New(Options{
		Stderr:   &bytes.Buffer{},
		Format:   "text",
		Level:    slog.LevelInfo,
		File:     combined,
		Rotation: Rotation{MaxSizeMB: 1, Compress: false}, // 1 MB, no async gzip to avoid races
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger := slog.New(h)

	// Two ~600 KB records: the second exceeds the 1 MB cap and forces a rollover.
	big := strings.Repeat("x", 600*1024)
	logger.Info(big)
	logger.Info(big)
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var logs int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "silo") && strings.HasSuffix(e.Name(), ".log") {
			logs++
		}
	}
	if logs < 2 {
		t.Fatalf("expected at least 2 log files after rotation (current + rotated), got %d: %v", logs, entries)
	}
}

// TestNewUnwritablePathErrors ensures a bad SILO_LOG_FILE surfaces as an error
// from New (so buildBaseHandler's stderr fallback engages) rather than silently
// no-opping at first write.
func TestNewUnwritablePathErrors(t *testing.T) {
	dir := t.TempDir()
	// Make a regular file, then try to nest a log path beneath it: MkdirAll must
	// fail because a path component is a file, not a directory.
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, _, err := New(Options{
		Stderr: &bytes.Buffer{},
		Format: "text",
		Level:  slog.LevelInfo,
		File:   filepath.Join(blocker, "sub", "silo.log"),
	})
	if err == nil {
		t.Fatal("expected New to return an error for an unwritable log path, got nil")
	}
}

func TestMultiCloserPropagatesFirstErrorAndClosesAll(t *testing.T) {
	sentinel := errors.New("boom")
	var closed []string
	stub := func(name string, err error) io.Closer {
		return closerFunc(func() error { closed = append(closed, name); return err })
	}
	mc := multiCloser{
		stub("a", nil),
		stub("b", sentinel),
		nil, // nil entries must be skipped, not panic
		stub("c", errors.New("second")),
	}
	if err := mc.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close returned %v, want first error %v", err, sentinel)
	}
	// Every non-nil closer must still have run despite the earlier failure.
	if len(closed) != 3 || closed[0] != "a" || closed[1] != "b" || closed[2] != "c" {
		t.Fatalf("expected all closers invoked in order, got %v", closed)
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
