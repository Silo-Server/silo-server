package playback

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestCache builds a cache rooted under a temp transcode dir and returns
// it with the path of a fake source media file.
func newTestCache(t *testing.T) (*SubtitleCache, string) {
	t.Helper()
	base := t.TempDir()
	source := filepath.Join(base, "movie.mkv")
	if err := os.WriteFile(source, []byte("fake mkv contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewSubtitleCache(func() string { return base }), source
}

// fillEntry populates the cache for source+track with the given payload via
// the real BeginFill → Tee → Commit path.
func fillEntry(t *testing.T, c *SubtitleCache, source string, track int, payload string) {
	t.Helper()
	fill := c.BeginFill(source, track)
	if fill == nil {
		t.Fatalf("BeginFill returned nil for track %d", track)
	}
	if _, err := fill.Tee(io.Discard).Write([]byte(payload)); err != nil {
		t.Fatalf("tee write: %v", err)
	}
	if err := fill.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func readAllAndClose(t *testing.T, f *os.File) string {
	t.Helper()
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSubtitleCacheMissThenHit(t *testing.T) {
	c, source := newTestCache(t)

	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("expected miss on empty cache")
	}

	fillEntry(t, c, source, 0, "PGS DATA TRACK 0")

	f, modTime, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("expected hit after commit")
	}
	if got := readAllAndClose(t, f); got != "PGS DATA TRACK 0" {
		t.Fatalf("cached content = %q", got)
	}
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !modTime.Equal(src.ModTime()) {
		t.Fatalf("hit modTime = %v, want source mtime %v", modTime, src.ModTime())
	}

	// A different track ordinal is a distinct entry.
	if _, _, ok := c.Lookup(source, 1); ok {
		t.Fatal("expected miss for uncached track ordinal")
	}
}

func TestSubtitleCacheInvalidatedBySourceMtime(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "old extract")

	newTime := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(source, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("expected miss after source mtime changed")
	}

	// Re-filling under the new source identity overwrites, and the stale
	// sibling entry is cleaned up.
	fillEntry(t, c, source, 0, "new extract")
	f, _, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("expected hit after refill")
	}
	if got := readAllAndClose(t, f); got != "new extract" {
		t.Fatalf("cached content = %q", got)
	}
	if n := countCacheEntries(t, c); n != 1 {
		t.Fatalf("stale sibling not removed: %d entries", n)
	}
}

func TestSubtitleCacheInvalidatedBySourceSize(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "old extract")

	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("different length contents!"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Restore the original mtime so only size differs.
	if err := os.Chtimes(source, src.ModTime(), src.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("expected miss after source size changed")
	}
}

func TestSubtitleCacheDiscardLeavesNothing(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.BeginFill(source, 0)
	if fill == nil {
		t.Fatal("BeginFill returned nil")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("partial byt")); err != nil {
		t.Fatal(err)
	}
	fill.Discard()

	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("discarded fill must not be served")
	}
	if n := countCacheFiles(t, c); n != 0 {
		t.Fatalf("discard left %d files (temp not removed?)", n)
	}
}

func TestSubtitleCacheCommitRefusesChangedSource(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.BeginFill(source, 0)
	if fill == nil {
		t.Fatal("BeginFill returned nil")
	}
	if _, err := fill.Tee(io.Discard).Write([]byte("extract from old source")); err != nil {
		t.Fatal(err)
	}
	// Source replaced mid-extract.
	newTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(source, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := fill.Commit(); err == nil {
		t.Fatal("Commit must refuse when source changed mid-fill")
	}
	if n := countCacheFiles(t, c); n != 0 {
		t.Fatalf("refused commit left %d files", n)
	}
}

func TestSubtitleCacheTeeWriteFailureKeepsServingClient(t *testing.T) {
	c, source := newTestCache(t)
	fill := c.BeginFill(source, 0)
	if fill == nil {
		t.Fatal("BeginFill returned nil")
	}
	// Force temp-file writes to fail (simulates disk full).
	fill.tmp.Close()

	var client strings.Builder
	n, err := fill.Tee(&client).Write([]byte("bytes for the viewer"))
	if err != nil || n != len("bytes for the viewer") {
		t.Fatalf("client write must succeed despite cache failure: n=%d err=%v", n, err)
	}
	if client.String() != "bytes for the viewer" {
		t.Fatalf("client got %q", client.String())
	}
	if err := fill.Commit(); err == nil {
		t.Fatal("Commit must fail after tee write error")
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("failed fill must not be served")
	}
}

func TestSubtitleCacheEvictionUnderCap(t *testing.T) {
	c, source := newTestCache(t)
	c.maxBytes = 25 // each payload below is 10 bytes

	base := time.Now().Add(-time.Hour)
	for track := 0; track < 3; track++ {
		fillEntry(t, c, source, track, fmt.Sprintf("0123456%03d", track))
		// Pin distinct LRU mtimes: track 0 oldest.
		path := entryPath(t, c, source, track)
		mt := base.Add(time.Duration(track) * time.Minute)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// 4th commit (10 bytes) pushes the total to 40 > 25; eviction must
	// remove the two oldest entries (tracks 0 and 1) to get back to 20.
	fillEntry(t, c, source, 3, "0123456003")

	for track, want := range map[int]bool{0: false, 1: false, 2: true, 3: true} {
		_, _, ok := c.Lookup(source, track)
		if ok != want {
			t.Errorf("track %d cached = %v, want %v", track, ok, want)
		}
	}
}

func TestSubtitleCacheCoalescing(t *testing.T) {
	c, source := newTestCache(t)

	first := c.BeginFill(source, 0)
	if first == nil {
		t.Fatal("first BeginFill returned nil")
	}
	if second := c.BeginFill(source, 0); second != nil {
		second.Discard()
		t.Fatal("second BeginFill for in-flight track must return nil")
	}
	// A different track is independent.
	other := c.BeginFill(source, 1)
	if other == nil {
		t.Fatal("BeginFill for a different track must not be blocked")
	}
	other.Discard()

	if _, err := first.Tee(io.Discard).Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	// Slot released after commit.
	if again := c.BeginFill(source, 0); again == nil {
		t.Fatal("BeginFill must work again after Commit")
	} else {
		again.Discard()
	}
}

func TestSubtitleCacheCoalescingConcurrent(t *testing.T) {
	c, source := newTestCache(t)

	const workers = 16
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fills []*SubtitleCacheFill
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if f := c.BeginFill(source, 0); f != nil {
				mu.Lock()
				fills = append(fills, f)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(fills) != 1 {
		t.Fatalf("exactly one concurrent BeginFill must win, got %d", len(fills))
	}
	fills[0].Discard()
}

func TestServeSUPExtractCacheFlow(t *testing.T) {
	c, source := newTestCache(t)

	extractCalls := 0
	extract := func(dst io.Writer) error {
		extractCalls++
		_, err := dst.Write([]byte("SUP PAYLOAD"))
		return err
	}

	// First request: miss → streamed 200 with no-store, entry committed.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	if err := c.ServeSUPExtract(rec, req, source, 0, extract); err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatalf("extract calls = %d", extractCalls)
	}
	if rec.Body.String() != "SUP PAYLOAD" {
		t.Fatalf("miss body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("miss Cache-Control = %q", cc)
	}

	// Second request: hit → served from cache, no extract, revalidatable.
	rec = httptest.NewRecorder()
	if err := c.ServeSUPExtract(rec, req, source, 0, extract); err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatal("cache hit must not invoke extract")
	}
	if rec.Body.String() != "SUP PAYLOAD" {
		t.Fatalf("hit body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("hit Cache-Control = %q", cc)
	}
	if rec.Header().Get("Last-Modified") == "" {
		t.Fatal("hit must carry Last-Modified")
	}
	if cl := rec.Header().Get("Content-Length"); cl != "11" {
		t.Fatalf("hit Content-Length = %q", cl)
	}

	// Range request against the cached entry.
	rec = httptest.NewRecorder()
	rangeReq := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	rangeReq.Header.Set("Range", "bytes=4-10")
	if err := c.ServeSUPExtract(rec, rangeReq, source, 0, extract); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "PAYLOAD" {
		t.Fatalf("range: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestServeSUPExtractWindowedBypassesCache(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "FULL TRACK")

	extractCalls := 0
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1", nil)
	err := c.ServeSUPExtract(rec, req, source, 0, func(dst io.Writer) error {
		extractCalls++
		_, err := dst.Write([]byte("WINDOW SLICE"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatal("windowed request must run its own extract even when cached")
	}
	if rec.Body.String() != "WINDOW SLICE" {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
	// The full-track entry must be untouched.
	f, _, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("full-track entry lost")
	}
	if got := readAllAndClose(t, f); got != "FULL TRACK" {
		t.Fatalf("full-track entry corrupted: %q", got)
	}
}

func TestServeSUPExtractDiscardsOnExtractError(t *testing.T) {
	c, source := newTestCache(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	wantErr := errors.New("ffmpeg exploded")
	err := c.ServeSUPExtract(rec, req, source, 0, func(dst io.Writer) error {
		_, _ = dst.Write([]byte("PARTIAL"))
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
	if _, _, ok := c.Lookup(source, 0); ok {
		t.Fatal("partial extract must not be cached")
	}
	if n := countCacheFiles(t, c); n != 0 {
		t.Fatalf("failed extract left %d files", n)
	}
}

func TestServeSUPExtractNilCacheStreams(t *testing.T) {
	var c *SubtitleCache
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	err := c.ServeSUPExtract(rec, req, "/nonexistent.mkv", 0, func(dst io.Writer) error {
		_, err := dst.Write([]byte("UNCACHED"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "UNCACHED" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// entryPath computes the committed entry path for source+track.
func entryPath(t *testing.T, c *SubtitleCache, source string, track int) string {
	t.Helper()
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(c.dir(), subtitleCacheKey(source, track, src.ModTime(), src.Size()))
}

// countCacheEntries counts committed .sup entries in the cache dir.
func countCacheEntries(t *testing.T, c *SubtitleCache) int {
	t.Helper()
	return countMatching(t, c, func(name string) bool {
		return strings.HasSuffix(name, ".sup") && !strings.Contains(name, ".part-")
	})
}

// countCacheFiles counts every file in the cache dir, temp files included.
func countCacheFiles(t *testing.T, c *SubtitleCache) int {
	t.Helper()
	return countMatching(t, c, func(string) bool { return true })
}

func countMatching(t *testing.T, c *SubtitleCache, match func(string) bool) int {
	t.Helper()
	entries, err := os.ReadDir(c.dir())
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if match(e.Name()) {
			n++
		}
	}
	return n
}
