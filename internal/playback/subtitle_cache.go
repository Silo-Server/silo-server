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

// SubtitleCache stores canonical full-track subtitle extracts on disk so
// repeat selections of the same embedded track don't re-run a whole-file
// ffmpeg demux (minutes for a large remux). Only extracts whose effective
// ffmpeg argv has no seek or duration are cached; partial requests may use a
// canonical cached artifact as their input but are never published themselves.
//
// Entries are keyed by schema version, source file path, subtitle stream
// ordinal, resolved output codec+muxer, and source mtime+size, all encoded in
// the cache filename. Invalidation is therefore implicit when any dimension
// changes. Entry recency for LRU is tracked by bumping the cache file's mtime
// on every hit (portable, unlike atime on noatime/relatime mounts).
//
// Concurrency: the first requester of an uncached track streams the extract
// progressively to its client while teeing bytes into a temp file that is
// atomically renamed into the cache on clean ffmpeg exit (and discarded on
// any error, so a partial entry is never served). Concurrent requesters for
// the same track and output profile while a fill is in flight simply run their
// own un-teed extract — no worse than today's behavior, and it avoids making a
// viewer's first-byte latency depend on another client's connection.
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
	// subtitleCacheSchemaVersion invalidates artifacts when extraction argv or
	// artifact semantics change in a way source mtime+size cannot detect.
	subtitleCacheSchemaVersion = 1
	// defaultSubtitleCacheMaxBytes caps all cached subtitle formats at 2 GiB.
	// Bitmap tracks dominate sizing at roughly 15-80 MB apiece; text tracks
	// are much smaller.
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

// ffmpeg muxer names for the subtitle formats an extract can produce (see
// StreamExtractOutput), plus the on-disk extension and the source codec the
// PGS-only entry points key on. Named because the extraction plan, the cache
// key, and the response Content-Type all have to agree on the same spellings.
const (
	subtitleFormatWebVTT = "webvtt"
	subtitleFormatASS    = "ass"
	subtitleFormatSUP    = "sup"
	// subtitleExtVTT is the file extension for a webvtt artifact; the ffmpeg
	// muxer name and the conventional extension differ only for this format.
	subtitleExtVTT          = "vtt"
	subtitleCodecPGS        = "hdmv_pgs_subtitle"
	subtitleTypeOctetStream = "application/octet-stream"
)

// SubtitleExtractFunc runs one ffmpeg subtitle extract described by opts, writing
// output to opts.Writer. Production callers pass StreamExtractSubtitle;
// tests substitute fakes. The cache invokes it with the caller's options
// rewritten as needed (tee writer for fills, cached-artifact input for partial
// serves, cleared window for background warms).
type SubtitleExtractFunc func(ctx context.Context, opts StreamExtractOpts) error

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

// ServeExtract serves one embedded subtitle extract (opts.Writer is ignored;
// the cache supplies it). Canonical requests may fill the cache on a miss.
// Requests whose effective ffmpeg argv applies seek or duration are partial:
// they never fill, re-extract from a cached canonical artifact on a hit, and
// trigger a canonical background warm on a miss.
//
// PGS cache hits retain the existing http.ServeContent behavior. Text hits
// deliberately use a plain copy with no-store, matching cold-path HTTP
// semantics without adding Range, validators, or Content-Length based on cache
// warmth. A nil receiver disables caching and just streams.
//
// The caller sets any extra response headers (e.g. CORS) before calling.
// The returned error is the extract error; cache hits return nil.
func (c *SubtitleCache) ServeExtract(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SubtitleExtractFunc) error {
	plan := streamExtractPlanFor(opts)
	if plan.partial() {
		return c.serveWindowed(w, r, opts, plan, extract)
	}

	if cached, modTime, ok := c.lookup(opts); ok {
		defer func() { _ = cached.Close() }()
		slog.DebugContext(r.Context(), "subtitle stream served from cache",
			"input", opts.InputPath, "track", opts.TrackIndex)
		w.Header().Set("Content-Type", subtitleContentType(plan.outFormat))
		if plan.outFormat == subtitleFormatSUP {
			w.Header().Set("Cache-Control", "private, no-cache")
			http.ServeContent(w, r, "", modTime, cached)
			return nil
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return copyAndFlush(w, cached)
	}

	w.Header().Set("Content-Type", subtitleContentType(plan.outFormat))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// BeginFill returns nil when another fill for this track is already in
	// flight (or the cache dir is unusable); this request then streams its
	// own uncached extract.
	fill := c.beginFill(opts)
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

// serveWindowed streams a partial request. Its effective argv is noncanonical,
// so its output is never cached itself, but the cache still speeds it up: with
// a committed full-track entry the extract's input is
// rewritten to the cached artifact (small enough that the -ss scan is fast
// versus re-demuxing a multi-GB source); without one, a background warm is
// started so later windows — the client re-fetches on every seek — hit the
// fast path.
func (c *SubtitleCache) serveWindowed(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, plan streamExtractPlan, extract SubtitleExtractFunc) error {
	if cachedPath, _, ok := c.cachedEntryPath(opts); ok {
		slog.DebugContext(r.Context(), "windowed subtitle extract using cached full track",
			"input", opts.InputPath, "track", opts.TrackIndex, "cache_entry", cachedPath)
		opts.InputPath = cachedPath
		opts.ExtractedInputFormat = plan.outFormat
	} else {
		c.WarmInBackground(opts, extract)
	}

	w.Header().Set("Content-Type", subtitleContentType(plan.outFormat))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	opts.Writer = w
	return extract(r.Context(), opts)
}

// WarmInBackground starts a detached full-track extract that fills the cache
// entry for opts' source+track+output profile, so future partial requests can
// extract from the small cached artifact instead of the original file. The
// warm runs on a background context with a generous timeout — it must survive
// the request that triggered it. BeginFill's in-flight coalescing guarantees at
// most one fill per output profile (a concurrent client-driven fill wins and the warm is
// skipped), and warmSem bounds warms server-wide: beyond the budget the warm
// is dropped, not queued — the next windowed miss re-attempts it. A nil
// receiver is a no-op.
func (c *SubtitleCache) WarmInBackground(opts StreamExtractOpts, extract SubtitleExtractFunc) {
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
	// Full-track options: the warm ignores the triggering request's window
	// and writes only to the cache temp file (no response writer).
	opts.SeekSeconds = 0
	opts.DurationSeconds = 0
	opts.AllowWindow = false
	opts.ExtractedInputFormat = ""
	fill := c.beginFill(opts)
	if fill == nil {
		// Another fill (client-driven or a previous warm) is already in
		// flight, or the cache is unusable — either way, nothing to do.
		<-c.warmSem
		return
	}
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

type subtitleCacheProfile struct {
	codec  string
	format string
}

func subtitleProfile(opts StreamExtractOpts) subtitleCacheProfile {
	plan := streamExtractPlanFor(opts)
	return subtitleCacheProfile{codec: plan.outCodec, format: plan.outFormat}
}

// subtitleCacheKeyPrefix identifies a source file + track ordinal + resolved
// output profile regardless of source version. Including the profile keeps,
// for example, ASS and WebVTT artifacts for the same source track independent.
func subtitleCacheKeyPrefix(inputPath string, trackIndex int, profile subtitleCacheProfile) string {
	sum := sha256.Sum256([]byte(inputPath))
	return fmt.Sprintf("v%d-%x-s%d-%s-%s-", subtitleCacheSchemaVersion, sum[:12], trackIndex, profile.codec, profile.format)
}

func subtitleCacheKey(inputPath string, trackIndex int, profile subtitleCacheProfile, mtime time.Time, size int64) string {
	return fmt.Sprintf("%s%d-%d.%s", subtitleCacheKeyPrefix(inputPath, trackIndex, profile), mtime.UnixNano(), size, subtitleCacheExtension(profile.format))
}

func subtitleCacheExtension(format string) string {
	if format == subtitleFormatWebVTT {
		return subtitleExtVTT
	}
	return format
}

func subtitleContentType(format string) string {
	switch format {
	case subtitleFormatASS:
		return "text/x-ssa; charset=utf-8"
	case subtitleFormatWebVTT:
		return "text/vtt; charset=utf-8"
	default:
		return subtitleTypeOctetStream
	}
}

// Lookup opens the cached canonical PGS extract for the source and track.
// It is retained as a focused cache primitive; format-aware serving uses
// lookup with the caller's resolved extraction options.
//
// mtime or size mismatch means the entry (if any) is stale and reads as a
// miss. On a hit the returned modTime is the *source* file's mtime — stable
// across hits, suitable for Last-Modified — while the cache file's own mtime
// is bumped to record recency for LRU eviction. The caller owns closing the
// returned file.
func (c *SubtitleCache) Lookup(inputPath string, trackIndex int) (f *os.File, modTime time.Time, ok bool) {
	return c.lookup(StreamExtractOpts{
		InputPath:   inputPath,
		TrackIndex:  trackIndex,
		SourceCodec: subtitleCodecPGS,
	})
}

func (c *SubtitleCache) lookup(opts StreamExtractOpts) (f *os.File, modTime time.Time, ok bool) {
	path, modTime, ok := c.cachedEntryPath(opts)
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
func (c *SubtitleCache) cachedEntryPath(opts StreamExtractOpts) (path string, srcModTime time.Time, ok bool) {
	dir := c.dir()
	if dir == "" {
		return "", time.Time{}, false
	}
	src, err := os.Stat(opts.InputPath)
	if err != nil {
		return "", time.Time{}, false
	}
	path = filepath.Join(dir, subtitleCacheKey(opts.InputPath, opts.TrackIndex, subtitleProfile(opts), src.ModTime(), src.Size()))
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
	profile    subtitleCacheProfile
	srcMtime   time.Time
	srcSize    int64
	tmp        *os.File
	// failed flips when a temp-file write errors (e.g. disk full); the tee
	// keeps serving the client and Commit refuses to publish the entry.
	failed bool
}

// BeginFill reserves the in-flight slot for the given PGS track and creates the
// temp file the tee will write into. Returns nil — meaning "stream without
// caching" — when caching is disabled, the source can't be stat'ed, the
// cache directory can't be created, or another fill for the same track is
// already in flight.
func (c *SubtitleCache) BeginFill(inputPath string, trackIndex int) *SubtitleCacheFill {
	return c.beginFill(StreamExtractOpts{
		InputPath:   inputPath,
		TrackIndex:  trackIndex,
		SourceCodec: subtitleCodecPGS,
	})
}

func (c *SubtitleCache) beginFill(opts StreamExtractOpts) *SubtitleCacheFill {
	dir := c.dir()
	if dir == "" {
		return nil
	}
	src, err := os.Stat(opts.InputPath)
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("subtitle cache dir create failed", "dir", dir, "error", err)
		return nil
	}
	profile := subtitleProfile(opts)
	key := subtitleCacheKey(opts.InputPath, opts.TrackIndex, profile, src.ModTime(), src.Size())

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
		inputPath:  opts.InputPath,
		trackIndex: opts.TrackIndex,
		profile:    profile,
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

	f.c.removeStaleSiblings(dir, f.inputPath, f.trackIndex, f.profile, f.key)
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

// removeStaleSiblings deletes committed entries for the same
// source+track+output profile with a different mtime/size suffix. Other output
// profiles remain valid siblings and must coexist.
func (c *SubtitleCache) removeStaleSiblings(dir, inputPath string, trackIndex int, profile subtitleCacheProfile, keepKey string) {
	prefix := subtitleCacheKeyPrefix(inputPath, trackIndex, profile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keepKey || !strings.HasPrefix(name, prefix) || strings.Contains(name, ".part-") {
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
		if !isCommittedSubtitleCacheEntry(e.Name()) {
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

func isCommittedSubtitleCacheEntry(name string) bool {
	if strings.Contains(name, ".part-") {
		return false
	}
	return strings.HasSuffix(name, ".sup") || strings.HasSuffix(name, ".ass") || strings.HasSuffix(name, ".vtt")
}
