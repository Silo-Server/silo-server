package playback

import (
	"context"
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

// supExtractOpts builds the base extract options a handler would pass to
// ServeSUPExtract for a PGS track.
func supExtractOpts(source string, track int) StreamExtractOpts {
	return StreamExtractOpts{
		InputPath:   source,
		TrackIndex:  track,
		SourceCodec: "hdmv_pgs_subtitle",
	}
}

// windowedSupOpts builds options for a windowed (?windowed=1) PGS request.
func windowedSupOpts(source string, track int, seek, duration float64) StreamExtractOpts {
	opts := supExtractOpts(source, track)
	opts.AllowWindow = true
	opts.SeekSeconds = seek
	opts.DurationSeconds = duration
	return opts
}

// waitForCacheEntry polls until the cache holds a committed entry for
// source+track — used to observe asynchronous background warms.
func waitForCacheEntry(t *testing.T, c *SubtitleCache, source string, track int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f, _, ok := c.Lookup(source, track); ok {
			_ = f.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cache entry for track %d never appeared", track)
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
	_ = fill.tmp.Close()

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
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		_, err := opts.Writer.Write([]byte("SUP PAYLOAD"))
		return err
	}

	// First request: miss → streamed 200 with no-store, entry committed.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	if err := c.ServeSUPExtract(rec, req, supExtractOpts(source, 0), extract); err != nil {
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
	if err := c.ServeSUPExtract(rec, req, supExtractOpts(source, 0), extract); err != nil {
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
	if err := c.ServeSUPExtract(rec, rangeReq, supExtractOpts(source, 0), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "PAYLOAD" {
		t.Fatalf("range: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// A windowed request against a cached track must run its extract with the
// cached .sup as input (small file → near-instant window) instead of
// re-demuxing the original media, must never publish its sliced output as a
// cache entry, and must bump the entry's LRU recency.
func TestServeSUPExtractWindowedUsesCachedTrack(t *testing.T) {
	c, source := newTestCache(t)
	fillEntry(t, c, source, 0, "FULL TRACK")

	// Age the entry so the LRU recency bump is observable.
	entry := entryPath(t, c, source, 0)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatal(err)
	}

	var got StreamExtractOpts
	extractCalls := 0
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=1200&duration=3600", nil)
	err := c.ServeSUPExtract(rec, req, windowedSupOpts(source, 0, 1200, 3600), func(_ context.Context, opts StreamExtractOpts) error {
		extractCalls++
		got = opts
		_, err := opts.Writer.Write([]byte("WINDOW SLICE"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if extractCalls != 1 {
		t.Fatalf("extract calls = %d", extractCalls)
	}
	if rec.Body.String() != "WINDOW SLICE" {
		t.Fatalf("windowed body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("windowed Cache-Control = %q", cc)
	}
	if got.InputPath != entry {
		t.Fatalf("windowed extract input = %q, want cached entry %q", got.InputPath, entry)
	}
	if !got.InputIsExtractedSup {
		t.Fatal("windowed extract from cache must set InputIsExtractedSup")
	}
	if got.SeekSeconds != 1200 || got.DurationSeconds != 3600 || !got.AllowWindow {
		t.Fatalf("window parameters not preserved: %+v", got)
	}

	// The full-track entry must be untouched, with recency bumped.
	f, _, ok := c.Lookup(source, 0)
	if !ok {
		t.Fatal("full-track entry lost")
	}
	if content := readAllAndClose(t, f); content != "FULL TRACK" {
		t.Fatalf("full-track entry corrupted: %q", content)
	}
	info, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old.Add(time.Minute)) {
		t.Fatalf("windowed serve must bump LRU recency: mtime = %v", info.ModTime())
	}
}

// A windowed miss must trigger exactly one detached background warm no
// matter how many windowed requests arrive while it runs, and once the warm
// commits, the next windowed request extracts from the cached track.
func TestServeSUPExtractWindowedMissWarmsOnce(t *testing.T) {
	c, source := newTestCache(t)

	var (
		mu          sync.Mutex
		warmOpts    []StreamExtractOpts
		windowOpts  []StreamExtractOpts
		warmRelease = make(chan struct{})
	)
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		if opts.AllowWindow {
			mu.Lock()
			windowOpts = append(windowOpts, opts)
			mu.Unlock()
			_, err := opts.Writer.Write([]byte("WINDOW SLICE"))
			return err
		}
		mu.Lock()
		warmOpts = append(warmOpts, opts)
		mu.Unlock()
		<-warmRelease
		_, err := opts.Writer.Write([]byte("FULL TRACK"))
		return err
	}

	// N windowed misses: each still streams its own windowed slice from the
	// original file; only the first starts a warm (BeginFill coalescing keeps
	// the rest out — deterministic because the in-flight slot is reserved
	// synchronously before ServeSUPExtract returns).
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=100&duration=3600", nil)
		if err := c.ServeSUPExtract(rec, req, windowedSupOpts(source, 0, 100, 3600), extract); err != nil {
			t.Fatal(err)
		}
		if rec.Body.String() != "WINDOW SLICE" {
			t.Fatalf("windowed body = %q", rec.Body.String())
		}
	}
	close(warmRelease)
	waitForCacheEntry(t, c, source, 0)

	mu.Lock()
	if len(warmOpts) != 1 {
		t.Fatalf("warm extracts = %d, want exactly 1", len(warmOpts))
	}
	warm := warmOpts[0]
	if warm.InputPath != source || warm.SeekSeconds != 0 || warm.DurationSeconds != 0 || warm.AllowWindow || warm.InputIsExtractedSup {
		t.Fatalf("warm must be a full-track extract of the original file: %+v", warm)
	}
	if len(windowOpts) != 4 {
		t.Fatalf("windowed extracts = %d, want 4", len(windowOpts))
	}
	for _, wo := range windowOpts {
		if wo.InputPath != source || wo.InputIsExtractedSup {
			t.Fatalf("pre-warm windowed extract must read the original file: %+v", wo)
		}
	}
	mu.Unlock()

	// Warm committed → the next windowed request reads the cached track.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=200&duration=3600", nil)
	if err := c.ServeSUPExtract(rec, req, windowedSupOpts(source, 0, 200, 3600), extract); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := windowOpts[len(windowOpts)-1]
	mu.Unlock()
	if last.InputPath != entryPath(t, c, source, 0) || !last.InputIsExtractedSup {
		t.Fatalf("post-warm windowed extract must read the cached track: %+v", last)
	}
}

// Warms beyond the server-wide slot budget are dropped, not queued, and a
// dropped warm must not leave an in-flight reservation behind.
func TestWarmInBackgroundSemaphoreDrop(t *testing.T) {
	c, source := newTestCache(t)

	release := make(chan struct{})
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		<-release
		_, err := opts.Writer.Write([]byte("FULL TRACK"))
		return err
	}

	// Occupy every warm slot (slots are acquired synchronously).
	for track := 0; track < subtitleCacheWarmSlots; track++ {
		c.WarmInBackground(supExtractOpts(source, track), extract)
	}
	// One more: dropped without reserving the track's in-flight slot.
	overflow := subtitleCacheWarmSlots
	c.WarmInBackground(supExtractOpts(source, overflow), extract)
	if fill := c.BeginFill(source, overflow); fill == nil {
		t.Fatal("dropped warm must not hold the in-flight slot")
	} else {
		fill.Discard()
	}

	close(release)
	for track := 0; track < subtitleCacheWarmSlots; track++ {
		waitForCacheEntry(t, c, source, track)
	}
	if _, _, ok := c.Lookup(source, overflow); ok {
		t.Fatal("dropped warm must not populate the cache")
	}

	// With slots free again, the overflow track's warm goes through.
	c.WarmInBackground(supExtractOpts(source, overflow), extract)
	waitForCacheEntry(t, c, source, overflow)
}

// A warm that races an already-in-flight client fill must skip (BeginFill
// coalescing) and release its warm slot for other tracks.
func TestWarmInBackgroundSkipsInFlightFill(t *testing.T) {
	c, source := newTestCache(t)

	clientFill := c.BeginFill(source, 0)
	if clientFill == nil {
		t.Fatal("BeginFill returned nil")
	}
	warmed := make(chan struct{}, 1)
	c.WarmInBackground(supExtractOpts(source, 0), func(_ context.Context, opts StreamExtractOpts) error {
		warmed <- struct{}{}
		_, err := opts.Writer.Write([]byte("WARM"))
		return err
	})

	// The skipped warm must have released its slot synchronously: all
	// subtitleCacheWarmSlots slots are still available.
	for track := 1; track <= subtitleCacheWarmSlots; track++ {
		c.WarmInBackground(supExtractOpts(source, track), func(_ context.Context, opts StreamExtractOpts) error {
			_, err := opts.Writer.Write([]byte("FULL TRACK"))
			return err
		})
	}
	for track := 1; track <= subtitleCacheWarmSlots; track++ {
		waitForCacheEntry(t, c, source, track)
	}

	select {
	case <-warmed:
		t.Fatal("warm for an in-flight track must not run")
	default:
	}
	clientFill.Discard()
}

func TestServeSUPExtractDiscardsOnExtractError(t *testing.T) {
	c, source := newTestCache(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	wantErr := errors.New("ffmpeg exploded")
	err := c.ServeSUPExtract(rec, req, supExtractOpts(source, 0), func(_ context.Context, opts StreamExtractOpts) error {
		_, _ = opts.Writer.Write([]byte("PARTIAL"))
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
	extract := func(_ context.Context, opts StreamExtractOpts) error {
		_, err := opts.Writer.Write([]byte("UNCACHED"))
		return err
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	if err := c.ServeSUPExtract(rec, req, supExtractOpts("/nonexistent.mkv", 0), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "UNCACHED" {
		t.Fatalf("body = %q", rec.Body.String())
	}

	// Windowed requests on a nil cache stream too (no lookup, no warm).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/sub.sup?windowed=1&position=10", nil)
	if err := c.ServeSUPExtract(rec, req, windowedSupOpts("/nonexistent.mkv", 0, 10, 3600), extract); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "UNCACHED" {
		t.Fatalf("windowed body = %q", rec.Body.String())
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
