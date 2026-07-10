package playback

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SubtitleCache stores full-track PGS (.sup) subtitle extracts on disk so
// repeat selections of the same embedded track don't re-run a whole-file
// ffmpeg demux (minutes for a large remux). Only complete, unwindowed .sup
// extracts are cached — VTT extracts are already windowed and fast, and ASS
// extracts are small; neither pays the full-demux cost PGS does.
//
// Entries are keyed by the source file path, subtitle stream ordinal, and the
// source's mtime+size, all encoded in the cache filename. Invalidation is
// therefore implicit: when the source changes, the lookup key changes and the
// old entry becomes garbage that eviction reclaims. Entry recency for LRU is
// tracked by bumping the cache file's mtime on every hit (portable, unlike
// atime which is often disabled via noatime/relatime mounts).
//
// Concurrency: the first requester of an uncached track streams the extract
// progressively to its client while teeing bytes into a temp file that is
// atomically renamed into the cache on clean ffmpeg exit (and discarded on
// any error, so a partial entry is never served). Concurrent requesters for
// the same track while a fill is in flight simply run their own un-teed
// extract — no worse than today's behavior, and it avoids making a viewer's
// first-byte latency depend on another client's connection.
type SubtitleCache struct {
	// transcodeDir returns the current transcode directory; the cache lives
	// in a subtitle-cache subdirectory beneath it, created lazily. An empty
	// return disables the cache for that call.
	transcodeDir func() string
	// maxBytes is the total-size eviction budget for committed entries.
	maxBytes int64

	mu       sync.Mutex
	inflight map[string]struct{}

	// warmSem bounds concurrent background warms server-wide (each warm
	// demuxes an entire source file — heavy sequential IO). Acquisition is
	// non-blocking: warms beyond the budget are dropped, not queued; the
	// next windowed miss for that track re-attempts the warm.
	warmSem chan struct{}
}

const (
	subtitleCacheDirName = "subtitle-cache"
	// defaultSubtitleCacheMaxBytes caps the cache at 2 GiB — PGS tracks run
	// 15-80 MB, so this holds a few dozen tracks.
	// TODO: expose as a config knob following the download.artifact_max_bytes
	// pattern (internal/config/config.go DownloadConfig.ArtifactMaxBytes).
	defaultSubtitleCacheMaxBytes = 2 << 30
	// stalePartMaxAge is how long an orphaned .part temp file (leftover from
	// a crash mid-fill) survives before eviction sweeps remove it.
	stalePartMaxAge = time.Hour
	// subtitleCacheWarmSlots caps concurrent background warms server-wide.
	// Two lets a second household stream warm while the first is still
	// demuxing, without letting a burst of playbacks saturate disk IO.
	subtitleCacheWarmSlots = 2
	// subtitleCacheWarmTimeout bounds a single background warm. A full-file
	// demux of a large remux on network storage can take minutes; anything
	// beyond this is stuck and should release its slot.
	subtitleCacheWarmTimeout = 30 * time.Minute
)

// SUPExtractFunc runs one ffmpeg subtitle extract described by opts, writing
// output to opts.Writer. Production callers pass StreamExtractSubtitle;
// tests substitute fakes. The cache invokes it with the caller's options
// rewritten as needed (tee writer for fills, cached-.sup input for windowed
// serves, cleared window for background warms).
type SUPExtractFunc func(ctx context.Context, opts StreamExtractOpts) error

// NewSubtitleCache builds a cache rooted under the transcode directory
// returned by transcodeDir at call time (so runtime config changes are
// honored). Pass nil to disable caching entirely.
func NewSubtitleCache(transcodeDir func() string) *SubtitleCache {
	return &SubtitleCache{
		transcodeDir: transcodeDir,
		maxBytes:     defaultSubtitleCacheMaxBytes,
		inflight:     make(map[string]struct{}),
		warmSem:      make(chan struct{}, subtitleCacheWarmSlots),
	}
}

// ServeSUPExtract serves the .sup extract for one source+track described by
// opts (opts.Writer is ignored; the cache supplies it). Full-track requests
// (no AllowWindow): a cache hit is served with http.ServeContent (Range
// support, Content-Length, Last-Modified from the source file's mtime,
// revalidatable instead of no-store); a miss invokes extract with a writer
// that streams to the client while teeing bytes into a temp file, atomically
// published as the cache entry on clean extract exit and discarded on any
// error (ffmpeg failure or client disconnect) — a partial entry is never
// served. Windowed requests (opts.AllowWindow): the output covers only a
// slice of the track, so it is never cached; but when the full-track entry
// already exists, the windowed extract runs against the small cached .sup
// instead of re-demuxing the original file, and when it doesn't, a detached
// background warm is kicked off so subsequent windows get that fast path. A
// nil receiver disables caching and just streams.
//
// The caller sets any extra response headers (e.g. CORS) before calling.
// The returned error is the extract error; cache hits return nil.
func (c *SubtitleCache) ServeSUPExtract(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SUPExtractFunc) error {
	if opts.AllowWindow {
		return c.serveWindowedSUP(w, r, opts, extract)
	}

	if cached, modTime, ok := c.Lookup(opts.InputPath, opts.TrackIndex); ok {
		defer func() { _ = cached.Close() }()
		slog.DebugContext(r.Context(), "subtitle stream served from cache",
			"input", opts.InputPath, "track", opts.TrackIndex)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "private, no-cache")
		http.ServeContent(w, r, "", modTime, cached)
		return nil
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// BeginFill returns nil when another fill for this track is already in
	// flight (or the cache dir is unusable); this request then streams its
	// own uncached extract.
	fill := c.BeginFill(opts.InputPath, opts.TrackIndex)
	var writer io.Writer = w
	if fill != nil {
		writer = fill.Tee(w)
	}

	opts.Writer = writer
	err := extract(r.Context(), opts)
	if fill != nil {
		if err != nil {
			fill.Discard()
		} else if commitErr := fill.Commit(); commitErr != nil {
			slog.WarnContext(r.Context(), "subtitle cache commit failed",
				"input", opts.InputPath, "track", opts.TrackIndex, "error", commitErr)
		}
	}
	return err
}

// serveWindowedSUP streams a windowed slice of the track. The output is a
// position-dependent slice so it is never cached itself, but the cache still
// speeds it up: with a committed full-track entry the extract's input is
// rewritten to the cached .sup (15-80 MB, so the -ss scan is near-instant
// versus re-demuxing a multi-GB source); without one, a background warm is
// started so later windows — the client re-fetches on every seek — hit the
// fast path.
func (c *SubtitleCache) serveWindowedSUP(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SUPExtractFunc) error {
	if cachedPath, _, ok := c.cachedEntryPath(opts.InputPath, opts.TrackIndex); ok {
		slog.DebugContext(r.Context(), "windowed subtitle extract using cached full track",
			"input", opts.InputPath, "track", opts.TrackIndex, "cache_entry", cachedPath)
		opts.InputPath = cachedPath
		opts.InputIsExtractedSup = true
	} else {
		c.WarmInBackground(opts, extract)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	opts.Writer = w
	return extract(r.Context(), opts)
}

// WarmInBackground starts a detached full-track extract that fills the cache
// entry for opts' source+track, so future windowed requests can extract from
// the small cached .sup instead of the original file. The warm runs on a
// background context with a generous timeout — it must survive the request
// that triggered it. BeginFill's in-flight coalescing guarantees at most one
// fill per track (a concurrent client-driven fill wins and the warm is
// skipped), and warmSem bounds warms server-wide: beyond the budget the warm
// is dropped, not queued — the next windowed miss re-attempts it. A nil
// receiver is a no-op.
func (c *SubtitleCache) WarmInBackground(opts StreamExtractOpts, extract SUPExtractFunc) {
	if c == nil || extract == nil {
		return
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("subtitle cache warm skipped: all warm slots busy",
			"input", opts.InputPath, "track", opts.TrackIndex)
		return
	}
	fill := c.BeginFill(opts.InputPath, opts.TrackIndex)
	if fill == nil {
		// Another fill (client-driven or a previous warm) is already in
		// flight, or the cache is unusable — either way, nothing to do.
		<-c.warmSem
		return
	}

	// Full-track options: the warm ignores the triggering request's window
	// and writes only to the cache temp file (no response writer).
	opts.SeekSeconds = 0
	opts.DurationSeconds = 0
	opts.AllowWindow = false
	opts.InputIsExtractedSup = false
	opts.Writer = fill.Tee(io.Discard)

	go func() {
		defer func() { <-c.warmSem }()
		ctx, cancel := context.WithTimeout(context.Background(), subtitleCacheWarmTimeout)
		defer cancel()

		start := time.Now()
		slog.Info("subtitle cache warm started",
			"input", opts.InputPath, "track", opts.TrackIndex)
		if err := extract(ctx, opts); err != nil {
			fill.Discard()
			slog.Warn("subtitle cache warm failed",
				"input", opts.InputPath, "track", opts.TrackIndex,
				"elapsed_ms", time.Since(start).Milliseconds(), "error", err)
			return
		}
		if err := fill.Commit(); err != nil {
			slog.Warn("subtitle cache warm commit failed",
				"input", opts.InputPath, "track", opts.TrackIndex, "error", err)
			return
		}
		slog.Info("subtitle cache warm finished",
			"input", opts.InputPath, "track", opts.TrackIndex,
			"elapsed_ms", time.Since(start).Milliseconds())
	}()
}

// dir resolves the cache directory, or "" when caching is disabled.
func (c *SubtitleCache) dir() string {
	if c == nil || c.transcodeDir == nil {
		return ""
	}
	base := c.transcodeDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, subtitleCacheDirName)
}

// subtitleCacheKeyPrefix identifies a source file + track ordinal regardless
// of source version; the full key appends mtime+size so a changed source
// yields a different filename.
func subtitleCacheKeyPrefix(inputPath string, trackIndex int) string {
	sum := sha256.Sum256([]byte(inputPath))
	return fmt.Sprintf("%x-s%d-", sum[:12], trackIndex)
}

func subtitleCacheKey(inputPath string, trackIndex int, mtime time.Time, size int64) string {
	return fmt.Sprintf("%s%d-%d.sup", subtitleCacheKeyPrefix(inputPath, trackIndex), mtime.UnixNano(), size)
}

// Lookup opens the cached full-track .sup extract for the given source file
// and subtitle stream ordinal. The source is stat'ed on every lookup: an
// mtime or size mismatch means the entry (if any) is stale and reads as a
// miss. On a hit the returned modTime is the *source* file's mtime — stable
// across hits, suitable for Last-Modified — while the cache file's own mtime
// is bumped to record recency for LRU eviction. The caller owns closing the
// returned file.
func (c *SubtitleCache) Lookup(inputPath string, trackIndex int) (f *os.File, modTime time.Time, ok bool) {
	path, modTime, ok := c.cachedEntryPath(inputPath, trackIndex)
	if !ok {
		return nil, time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	return f, modTime, true
}

// cachedEntryPath reports whether a committed entry exists for the given
// source+track and returns its path plus the source file's mtime. Like
// Lookup it stats the source on every call (a changed source reads as a
// miss) and bumps the entry's mtime to record recency for LRU eviction.
// Callers that hand the path to an external reader (ffmpeg) rather than
// opening it themselves use this instead of Lookup.
func (c *SubtitleCache) cachedEntryPath(inputPath string, trackIndex int) (path string, srcModTime time.Time, ok bool) {
	dir := c.dir()
	if dir == "" {
		return "", time.Time{}, false
	}
	src, err := os.Stat(inputPath)
	if err != nil {
		return "", time.Time{}, false
	}
	path = filepath.Join(dir, subtitleCacheKey(inputPath, trackIndex, src.ModTime(), src.Size()))
	if _, err := os.Stat(path); err != nil {
		return "", time.Time{}, false
	}
	// Recency bump for LRU. Best-effort: a failure (e.g. read-only remount)
	// only degrades eviction ordering, not correctness.
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		slog.Debug("subtitle cache recency bump failed", "path", path, "error", err)
	}
	return path, src.ModTime(), true
}

// SubtitleCacheFill is an in-progress cache population for one track. Bytes
// are written to a temp file via the writer returned by Tee; Commit renames
// it into place atomically, Discard throws it away. Exactly one of Commit or
// Discard must be called.
type SubtitleCacheFill struct {
	c          *SubtitleCache
	key        string
	inputPath  string
	trackIndex int
	srcMtime   time.Time
	srcSize    int64
	tmp        *os.File
	// failed flips when a temp-file write errors (e.g. disk full); the tee
	// keeps serving the client and Commit refuses to publish the entry.
	failed bool
}

// BeginFill reserves the in-flight slot for the given track and creates the
// temp file the tee will write into. Returns nil — meaning "stream without
// caching" — when caching is disabled, the source can't be stat'ed, the
// cache directory can't be created, or another fill for the same track is
// already in flight.
func (c *SubtitleCache) BeginFill(inputPath string, trackIndex int) *SubtitleCacheFill {
	dir := c.dir()
	if dir == "" {
		return nil
	}
	src, err := os.Stat(inputPath)
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("subtitle cache dir create failed", "dir", dir, "error", err)
		return nil
	}
	key := subtitleCacheKey(inputPath, trackIndex, src.ModTime(), src.Size())

	c.mu.Lock()
	if _, busy := c.inflight[key]; busy {
		c.mu.Unlock()
		return nil
	}
	c.inflight[key] = struct{}{}
	c.mu.Unlock()

	tmp, err := os.CreateTemp(dir, key+".part-*")
	if err != nil {
		c.release(key)
		slog.Warn("subtitle cache temp create failed", "dir", dir, "error", err)
		return nil
	}
	return &SubtitleCacheFill{
		c:          c,
		key:        key,
		inputPath:  inputPath,
		trackIndex: trackIndex,
		srcMtime:   src.ModTime(),
		srcSize:    src.Size(),
		tmp:        tmp,
	}
}

func (c *SubtitleCache) release(key string) {
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
}

// Tee wraps the response writer so every chunk also lands in the fill's temp
// file. The returned writer implements http.Flusher (delegating to w when w
// does), so copyAndFlush keeps flushing cues to the client in real time. A
// temp-file write failure never fails the response — the fill is marked
// failed and the client keeps streaming.
func (f *SubtitleCacheFill) Tee(w io.Writer) io.Writer {
	flusher, _ := w.(http.Flusher)
	return &subtitleTeeWriter{w: w, flusher: flusher, fill: f}
}

type subtitleTeeWriter struct {
	w       io.Writer
	flusher http.Flusher
	fill    *SubtitleCacheFill
}

func (t *subtitleTeeWriter) Write(p []byte) (int, error) {
	if !t.fill.failed {
		if _, err := t.fill.tmp.Write(p); err != nil {
			t.fill.failed = true
			slog.Warn("subtitle cache tee write failed; continuing uncached",
				"track", t.fill.trackIndex, "error", err)
		}
	}
	return t.w.Write(p)
}

func (t *subtitleTeeWriter) Flush() {
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

// Commit publishes the temp file as the cache entry: fsync, atomic rename,
// stale-sibling cleanup, then size-cap eviction. It refuses to publish (and
// discards instead) when a tee write failed or when the source file changed
// while the extract ran — a partial or mismatched entry must never be served.
func (f *SubtitleCacheFill) Commit() error {
	if f.failed {
		f.Discard()
		return errors.New("subtitle cache fill had write errors; discarded")
	}
	if src, err := os.Stat(f.inputPath); err != nil ||
		!src.ModTime().Equal(f.srcMtime) || src.Size() != f.srcSize {
		f.Discard()
		return errors.New("source file changed during extract; cache fill discarded")
	}
	defer f.c.release(f.key)

	tmpPath := f.tmp.Name()
	if err := f.tmp.Sync(); err != nil {
		f.closeAndRemoveTmp()
		return fmt.Errorf("sync subtitle cache temp: %w", err)
	}
	if err := f.tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close subtitle cache temp: %w", err)
	}
	dir := filepath.Dir(tmpPath)
	final := filepath.Join(dir, f.key)
	if err := os.Rename(tmpPath, final); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish subtitle cache entry: %w", err)
	}

	f.c.removeStaleSiblings(dir, f.inputPath, f.trackIndex, f.key)
	f.c.evict(dir)
	return nil
}

// Discard abandons the fill: the temp file is removed and the in-flight slot
// released. Safe to call after a failed Commit (idempotent enough — the temp
// file is already gone and re-removal is a no-op).
func (f *SubtitleCacheFill) Discard() {
	f.closeAndRemoveTmp()
	f.c.release(f.key)
}

func (f *SubtitleCacheFill) closeAndRemoveTmp() {
	_ = f.tmp.Close()
	if err := os.Remove(f.tmp.Name()); err != nil && !os.IsNotExist(err) {
		slog.Warn("subtitle cache temp remove failed", "path", f.tmp.Name(), "error", err)
	}
}

// removeStaleSiblings deletes committed entries for the same source+track
// with a different mtime/size suffix — the source was replaced, so those can
// never be served again.
func (c *SubtitleCache) removeStaleSiblings(dir, inputPath string, trackIndex int, keepKey string) {
	prefix := subtitleCacheKeyPrefix(inputPath, trackIndex)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keepKey || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".sup") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			slog.Warn("subtitle cache stale entry remove failed", "name", name, "error", err)
		}
	}
}

// evict is the scan-on-write LRU pass: when committed entries exceed the
// byte budget, the oldest-mtime entries are removed until the total fits.
// It also sweeps orphaned .part temp files older than stalePartMaxAge
// (crash leftovers). No background daemon — commits are rare enough that a
// directory scan per commit is cheap.
func (c *SubtitleCache) evict(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cacheEnt struct {
		path  string
		size  int64
		mtime time.Time
	}
	var (
		ents  []cacheEnt
		total int64
	)
	now := time.Now()
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if strings.Contains(e.Name(), ".part-") {
			if now.Sub(info.ModTime()) > stalePartMaxAge {
				_ = os.Remove(path)
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".sup") {
			continue
		}
		ents = append(ents, cacheEnt{path: path, size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
	}
	if total <= c.maxBytes {
		return
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].mtime.Before(ents[j].mtime) })
	for _, e := range ents {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(e.path); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("subtitle cache eviction remove failed", "path", e.path, "error", err)
			}
			continue
		}
		slog.Info("evicted cached subtitle track (LRU)", "path", e.path, "bytes", e.size)
		total -= e.size
	}
}
