package playback

import (
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
)

// NewSubtitleCache builds a cache rooted under the transcode directory
// returned by transcodeDir at call time (so runtime config changes are
// honored). Pass nil to disable caching entirely.
func NewSubtitleCache(transcodeDir func() string) *SubtitleCache {
	return &SubtitleCache{
		transcodeDir: transcodeDir,
		maxBytes:     defaultSubtitleCacheMaxBytes,
		inflight:     make(map[string]struct{}),
	}
}

// ServeSUPExtract serves the full-track .sup extract for one source+track.
// Cache hit: the entry is served with http.ServeContent (Range support,
// Content-Length, Last-Modified from the source file's mtime, revalidatable
// instead of no-store). Miss: extract is invoked with a writer that streams
// to the client while teeing bytes into a temp file, atomically published as
// the cache entry on clean extract exit and discarded on any error (ffmpeg
// failure or client disconnect) — a partial entry is never served. Windowed
// requests (?windowed=) bypass the cache entirely in both directions: their
// output covers only a slice of the track. A nil receiver disables caching
// and just streams.
//
// The caller sets any extra response headers (e.g. CORS) before calling.
// The returned error is the extract error; cache hits return nil.
func (c *SubtitleCache) ServeSUPExtract(w http.ResponseWriter, r *http.Request, inputPath string, trackIndex int, extract func(io.Writer) error) error {
	cacheable := c != nil && r.URL.Query().Get("windowed") == ""
	if cacheable {
		if cached, modTime, ok := c.Lookup(inputPath, trackIndex); ok {
			defer func() { _ = cached.Close() }()
			slog.DebugContext(r.Context(), "subtitle stream served from cache",
				"input", inputPath, "track", trackIndex)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Cache-Control", "private, no-cache")
			http.ServeContent(w, r, "", modTime, cached)
			return nil
		}
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// BeginFill returns nil when another fill for this track is already in
	// flight (or the cache dir is unusable); this request then streams its
	// own uncached extract.
	var fill *SubtitleCacheFill
	if cacheable {
		fill = c.BeginFill(inputPath, trackIndex)
	}
	var writer io.Writer = w
	if fill != nil {
		writer = fill.Tee(w)
	}

	err := extract(writer)
	if fill != nil {
		if err != nil {
			fill.Discard()
		} else if commitErr := fill.Commit(); commitErr != nil {
			slog.WarnContext(r.Context(), "subtitle cache commit failed",
				"input", inputPath, "track", trackIndex, "error", commitErr)
		}
	}
	return err
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
	dir := c.dir()
	if dir == "" {
		return nil, time.Time{}, false
	}
	src, err := os.Stat(inputPath)
	if err != nil {
		return nil, time.Time{}, false
	}
	path := filepath.Join(dir, subtitleCacheKey(inputPath, trackIndex, src.ModTime(), src.Size()))
	f, err = os.Open(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	// Recency bump for LRU. Best-effort: a failure (e.g. read-only remount)
	// only degrades eviction ordering, not correctness.
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		slog.Debug("subtitle cache recency bump failed", "path", path, "error", err)
	}
	return f, src.ModTime(), true
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
