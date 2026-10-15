package playback

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// Text subtitle burn-in of a relayed source. The libass subtitles filter
// opens its file itself, outside FFmpeg's input and its restrictions, so it
// must not open a transcode node's loopback URL. Instead the selected text
// track is extracted to an ASS script (text formats convert to ASS, ASS and
// SSA are copied), with -copyts so its events keep the container timestamps
// the transcode's frames carry, and the fonts attached to the source go to a
// fonts directory next to it. The filter then reads the script (si=0) with
// that fontsdir, which is what it would have found reading the source itself.
// A local source keeps the symlink alias (prepareSubtitleFilterInput).
//
// Extracting a track demuxes the whole source, which through the relay runs
// at the network's pace: tens of seconds for a film. So a staging belongs to
// the node, not to a request (RelayedSubtitleStaging): it runs detached, on
// a source handle of its own, and is kept in a node-level cache keyed by the
// source revision, track and codec, which every restart, seek, audio switch,
// fallback attempt and rebuild of a session on that source reuses. A start
// whose staging is not complete returns at once with a preparing session,
// which launches FFmpeg when the staging completes (TranscodeSession.preparing).
//
// One difference from libass reading the source: libass's filter loads only
// attachments whose MIME type names a font, while staging keeps every
// attachment isFontAttachment accepts, which includes one whose filename
// alone says it is a font (application/octet-stream with a .ttf name), once
// its bytes look like one. Core source access may so render with a font that
// local access ignores.

const (
	// stagedSubtitleFile and stagedSubtitleFontsDir are the extracted script
	// and the attached fonts, in a staging's directory.
	stagedSubtitleFile     = "selected.ass"
	stagedSubtitleFontsDir = "fonts"
	// stagedSubtitleMaxBytes bounds the extracted script, which streams to
	// disk: 256 MiB holds the largest real-world scripts (karaoke and
	// typesetting heavy ASS runs to tens of MiB) many times over while still
	// stopping a runaway extraction. stagedSubtitleProbeMaxBytes bounds the
	// attachment metadata.
	stagedSubtitleMaxBytes      = 256 << 20
	stagedSubtitleProbeMaxBytes = 1 << 20

	// The staging timeout: the extraction reads the whole source through the
	// relay. It allows two minutes, plus the source's size at
	// stagedSubtitleMinBytesPerSecond, at most stagedSubtitleMaxTimeout. 4 MiB/s
	// (about 34 Mbit/s) is a slow link between a node and its core, below the
	// bitrate of a UHD remux: a 4 GiB file gets about 19 minutes, and a
	// 60 GiB remux, which at that rate takes over four hours, the full four.
	// It bounds the staging itself, which no request waits on whole: starts
	// return while it runs, and requests for the session wait as they do for
	// a slow FFmpeg start.
	stagedSubtitleMinTimeout        = 2 * time.Minute
	stagedSubtitleMinBytesPerSecond = 4 << 20
	stagedSubtitleMaxTimeout        = 4 * time.Hour

	// stagedSubtitleFailureTTL is how long a staging that failed for a
	// passing reason (the relay, a timeout) answers the starts that join it
	// with its failure before a start stages again, so a burst of retries
	// does not read the whole source each time. A failure that follows from
	// the source's bytes (see deterministicStagingError) is kept until the
	// cache's sweep instead: staging that revision again fails the same way.
	stagedSubtitleFailureTTL = time.Minute

	// SubtitleStagingDirName is the staging cache's directory under a
	// transcode node's transcode directory, and
	// defaultSubtitleStagingMaxBytes its size bound.
	SubtitleStagingDirName         = "subtitle-staging"
	defaultSubtitleStagingMaxBytes = 1 << 30
	// stagingReserveBytes is what a staging in progress counts against the
	// cache's size bound: the most it may write, its script and fonts at
	// their bounds. A staging that cannot be given that is not begun.
	stagingReserveBytes = stagedSubtitleMaxBytes + maxSubtitleFontBytes

	// defaultSubtitleStagingSlots stagings run at once on a node, each an
	// FFmpeg reading a whole source through the relay. Up to
	// defaultSubtitleStagingQueue more starts wait for a slot, each within
	// its own request; further ones are refused (ErrSubtitleStagingBusy).
	defaultSubtitleStagingSlots = 2
	defaultSubtitleStagingQueue = 8
	// defaultStagingIdleGrace is how long a staging in progress that no
	// session leases any more runs on before it is canceled. It outlasts the
	// gap between a start that gave up waiting for its manifest and the
	// client's retry, which joins the staging.
	defaultStagingIdleGrace = 30 * time.Second

	// stagingKeyPrefix names the layout a staging directory is written in.
	// Every key starts with it, so a node adopts only the stagings a build
	// writing this layout left: change it with the layout.
	stagingKeyPrefix = "v1-"
	// stagingTempPrefix marks a staging still being written, or one moved
	// aside for removal; a complete one is renamed to its key.
	stagingTempPrefix = ".staging-"

	ffmpegNoStdinArg = "-nostdin"
	ffmpegMapArg     = "-map"
	ffmpegStdout     = "pipe:1"
	ttfFontExt       = ".ttf"
	utf8BOM          = "\xef\xbb\xbf"
	assScriptHeader  = "[Script Info]"
)

// ErrSubtitleStaging marks a session whose text subtitle could not be staged
// for burn-in. It is the session's WaitError, so a startup does not take it
// for a hardware failure and try another path on the same source.
var ErrSubtitleStaging = errors.New("stage subtitle")

// ErrSubtitleStagingBusy is a start whose subtitle staging could not begin:
// the node's staging slots and their queue are full, or the staging cache
// cannot hold another staging in progress. It is retryable.
var ErrSubtitleStagingBusy = fmt.Errorf("%w: the node's subtitle staging is busy", ErrSubtitleStaging)

// deterministicStagingError is a staging failure that follows from the
// source's bytes: FFmpeg failing on them, a track that is not an ASS script,
// a script or fonts past their bounds, attachments that differ from their
// probed sizes. Staging the same revision again fails the same way.
type deterministicStagingError struct{ err error }

func (e deterministicStagingError) Error() string { return e.err.Error() }
func (e deterministicStagingError) Unwrap() error { return e.err }

func deterministic(err error) error { return deterministicStagingError{err: err} }

// stagedSubtitleTimeout bounds staging the subtitle of a source of size bytes.
func stagedSubtitleTimeout(size int64) time.Duration {
	if size <= 0 {
		return stagedSubtitleMinTimeout
	}
	read := time.Duration(size / stagedSubtitleMinBytesPerSecond * int64(time.Second))
	return min(stagedSubtitleMinTimeout+read, stagedSubtitleMaxTimeout)
}

// textSubtitleBurnIn reports a session that burns in a text subtitle through
// the libass subtitles filter. Bitmap subtitles overlay the decoded stream.
func textSubtitleBurnIn(opts TranscodeOpts) bool {
	return opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0 && !NeedsBurnIn(opts.SubtitleCodec)
}

// stagesRelayedSubtitle reports a text burn-in of a relayed source, whose
// track must be staged before FFmpeg starts. Copied video renders no
// subtitle, so it stages nothing.
func stagesRelayedSubtitle(opts TranscodeOpts) bool {
	return textSubtitleBurnIn(opts) && !strings.EqualFold(opts.TargetCodecVideo, "copy") && nodesource.IsRemote(opts.InputPath)
}

// RelayedSubtitleStaging is a transcode node's cache of staged text
// subtitles, in a directory of its own under the node's transcode directory.
// Each staging is keyed by the layout version, the source revision, the
// track and its codec; one staging per key runs at a time, detached from the
// request that started it, on a source handle of its own, within
// stagedSubtitleTimeout.
//
// Admission is bounded three ways. At most slots stagings run at once; a
// start that needs a new one while they are taken waits within its own
// context, behind at most maxQueued others, and is refused beyond that.
// A staging in progress counts stagingReserveBytes against maxBytes, and one
// that cannot fit, after evicting what can be, is refused. And a staging in
// progress that no session leases any more is canceled after idleGrace.
//
// Sessions lease the staging they burn in, and a leased staging is never
// evicted. The cache keeps its completed stagings within maxBytes, evicting
// the least recently used unleased ones, and Sweep removes those unused for
// an age. Directories leave the cache by a rename aside under its mutex and
// are removed after it is released.
type RelayedSubtitleStaging struct {
	dir       string
	maxBytes  int64
	slots     chan struct{}
	maxQueued int
	idleGrace time.Duration

	mu      sync.Mutex
	entries map[string]*subtitleStaging
	// running holds the stagings in progress, including any canceled out
	// of entries that have not ended yet; each holds a slot and counts
	// stagingReserveBytes.
	running map[*subtitleStaging]struct{}
	queued  int
	asides  int

	// stage writes one staging into dir; tests replace it.
	stage func(ctx context.Context, job stagingJob, dir string) error
	// removeDir removes a directory that left the cache; tests replace it.
	removeDir func(string) error
	now       func() time.Time
}

// stagingJob is what one staging extracts.
type stagingJob struct {
	ffmpeg string
	input  string
	track  int
	codec  string
}

// subtitleStaging is one cache entry. done closes when the staging ends,
// after the cache has settled it: err, permanent, bytes and ended are set.
// ended, leases, lastUsed and idle are guarded by the cache's mutex.
type subtitleStaging struct {
	key    string
	dir    string
	done   chan struct{}
	cancel context.CancelFunc
	ended  bool
	err    error
	// permanent marks a failure kept until the sweep (see
	// stagedSubtitleFailureTTL).
	permanent bool
	endedAt   time.Time
	bytes     int64
	leases    int
	lastUsed  time.Time
	// idle cancels a staging in progress that lost its last lease.
	idle *time.Timer
}

// closed reports an ended staging without the cache's mutex.
func (e *subtitleStaging) closed() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// NewRelayedSubtitleStaging returns the staging cache in dir, creating it. It
// adopts the complete stagings a previous run of this layout left there,
// checking their content as staging does, and removes the rest.
func NewRelayedSubtitleStaging(dir string) (*RelayedSubtitleStaging, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("subtitle staging: %w", err)
	}
	c := &RelayedSubtitleStaging{
		dir:       dir,
		maxBytes:  defaultSubtitleStagingMaxBytes,
		slots:     make(chan struct{}, defaultSubtitleStagingSlots),
		maxQueued: defaultSubtitleStagingQueue,
		idleGrace: defaultStagingIdleGrace,
		entries:   make(map[string]*subtitleStaging),
		running:   make(map[*subtitleStaging]struct{}),
		stage:     stageSubtitle,
		removeDir: os.RemoveAll,
		now:       time.Now,
	}
	found, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("subtitle staging: %w", err)
	}
	for _, entry := range found {
		path := filepath.Join(dir, entry.Name())
		size, ready := verifiedStaging(path)
		if !ready || !strings.HasPrefix(entry.Name(), stagingKeyPrefix) {
			_ = os.RemoveAll(path)
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		done := make(chan struct{})
		close(done)
		c.entries[entry.Name()] = &subtitleStaging{key: entry.Name(), dir: path, done: done, ended: true, bytes: size, lastUsed: info.ModTime()}
	}
	c.mu.Lock()
	asides, _ := c.evictLocked(0)
	c.mu.Unlock()
	c.removeAll(asides)
	return c, nil
}

// Dir is the cache's directory.
func (c *RelayedSubtitleStaging) Dir() string { return c.dir }

// subtitleLease is a session's hold on a staging: while it holds it, the
// staging is not evicted, nor canceled while in progress.
type subtitleLease struct {
	c    *RelayedSubtitleStaging
	e    *subtitleStaging
	once sync.Once
}

// Done closes when the staging has ended.
func (l *subtitleLease) Done() <-chan struct{} { return l.e.done }

// ready reports a staging that completed.
func (l *subtitleLease) ready() bool { return l.e.closed() && l.e.err == nil }

// failed reports a staging that ended without completing.
func (l *subtitleLease) failed() bool { return l.e.closed() && l.e.err != nil }

// result is the staging's directory or its failure, once it has ended.
func (l *subtitleLease) result() (string, error) {
	<-l.e.done
	return l.e.dir, l.e.err
}

// release gives the staging up. The last lease's release makes a completed
// staging evictable, and starts the idle grace of one in progress.
func (l *subtitleLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		c := l.c
		c.mu.Lock()
		l.e.leases--
		l.e.lastUsed = c.now()
		if l.e.leases == 0 && !l.e.ended {
			e := l.e
			e.idle = time.AfterFunc(c.idleGrace, func() { c.cancelIdle(e) })
		}
		asides, _ := c.evictLocked(0)
		c.mu.Unlock()
		c.removeAll(asides)
	})
}

// cancelIdle cancels e, a staging in progress, unless a session leased it
// again. It leaves the cache at once, so the next start stages afresh rather
// than joining it or its cancellation.
func (c *RelayedSubtitleStaging) cancelIdle(e *subtitleStaging) {
	c.mu.Lock()
	if e.leases > 0 || e.ended || c.entries[e.key] != e {
		c.mu.Unlock()
		return
	}
	delete(c.entries, e.key)
	c.mu.Unlock()
	slog.Info("relayed subtitle staging canceled: no session uses it", "component", "playback", "staging", e.key)
	e.cancel()
}

// stagingKey names the staging of a source revision's track and codec.
func stagingKey(identity string, track int, codec string) string {
	sum := sha256.Sum256([]byte(identity + "\x00" + strconv.Itoa(track) + "\x00" + codec))
	return stagingKeyPrefix + hex.EncodeToString(sum[:16])
}

// acquireStagedSubtitle leases the staging of opts' selected track: complete,
// in progress (the lease waits for it), or failed and still remembered (the
// lease reports the failure). When there is none it begins one, once a slot
// and room in the cache are free; see RelayedSubtitleStaging.
func acquireStagedSubtitle(ctx context.Context, opts TranscodeOpts) (*subtitleLease, error) {
	c := opts.SubtitleStaging
	if c == nil {
		return nil, fmt.Errorf("%w: this process has no subtitle staging for a relayed source", ErrSubtitleStaging)
	}
	h, ok := nodesource.Lookup(opts.InputPath)
	if !ok {
		return nil, fmt.Errorf("%w: the source is not relayed", ErrSubtitleStaging)
	}
	identity := opts.SourceIdentity
	if identity == "" {
		identity = h.Identity()
	}
	codec := strings.ToLower(opts.SubtitleCodec)
	key := stagingKey(identity, opts.SubtitleTrackIndex, codec)

	c.mu.Lock()
	if lease := c.joinLocked(key); lease != nil {
		c.mu.Unlock()
		return lease, nil
	}
	select {
	case c.slots <- struct{}{}:
	default:
		if c.queued >= c.maxQueued {
			queued := c.queued
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: %d stagings run and %d starts wait for one", ErrSubtitleStagingBusy, cap(c.slots), queued)
		}
		c.queued++
		c.mu.Unlock()
		var waitErr error
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
		c.mu.Lock()
		c.queued--
		if waitErr != nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: waiting for a staging slot: %w", ErrSubtitleStagingBusy, waitErr)
		}
		// Another start may have begun this staging meanwhile.
		if lease := c.joinLocked(key); lease != nil {
			<-c.slots
			c.mu.Unlock()
			return lease, nil
		}
	}
	asides, fits := c.evictLocked(stagingReserveBytes)
	if !fits {
		<-c.slots
		c.mu.Unlock()
		c.removeAll(asides)
		return nil, fmt.Errorf("%w: the staging cache has no room for another staging", ErrSubtitleStagingBusy)
	}
	now := c.now()
	// Detached from the request: the staging serves every session on this
	// source and track, and outlives the start that began it.
	stageCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e := &subtitleStaging{key: key, dir: filepath.Join(c.dir, key), done: make(chan struct{}), cancel: cancel, leases: 1, lastUsed: now}
	c.entries[key] = e
	c.running[e] = struct{}{}
	job := stagingJob{ffmpeg: ResolveFFmpegPath(opts.FFmpegPath), track: opts.SubtitleTrackIndex, codec: codec}
	go c.run(stageCtx, e, h, job)
	c.mu.Unlock()
	c.removeAll(asides)
	return &subtitleLease{c: c, e: e}, nil
}

// leaseStagedSubtitle is acquireStagedSubtitle for a session, which cannot
// burn in a staging that failed: a failure still remembered answers the
// start's retries with that error until stagedSubtitleFailureTTL ends.
func leaseStagedSubtitle(ctx context.Context, opts TranscodeOpts) (*subtitleLease, error) {
	lease, err := acquireStagedSubtitle(ctx, opts)
	if err != nil {
		return nil, err
	}
	if lease.failed() {
		_, err := lease.result()
		lease.release()
		return nil, err
	}
	return lease, nil
}

// joinLocked leases the staging under key when one is in progress, complete,
// or a failure still remembered; nil when a new staging must begin.
func (c *RelayedSubtitleStaging) joinLocked(key string) *subtitleLease {
	e, ok := c.entries[key]
	if !ok {
		return nil
	}
	now := c.now()
	switch {
	case !e.ended:
	case e.err != nil && (e.permanent || now.Sub(e.endedAt) < stagedSubtitleFailureTTL):
	case e.err == nil && isRealDir(e.dir):
	default:
		// A failure past its time, or a staging removed under the cache:
		// stage again.
		if e.leases == 0 {
			delete(c.entries, key)
		}
		return nil
	}
	e.leases++
	e.lastUsed = now
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
	return &subtitleLease{c: c, e: e}
}

// run stages e on a handle of its own, reopened from h's grant, so the
// staging survives the job that began it. It holds one of the cache's slots,
// which it gives back when it ends.
func (c *RelayedSubtitleStaging) run(ctx context.Context, e *subtitleStaging, h *nodesource.Handle, job stagingJob) {
	defer e.cancel()
	started := c.now()
	ctx, cancel := context.WithTimeout(ctx, stagedSubtitleTimeout(h.Size()))
	defer cancel()
	scratch, size, relayFailed, err := c.stageAside(ctx, e, h, job)
	if scratch != "" {
		defer func() { _ = c.removeDir(scratch) }()
	}
	// A failure is the source's own only when neither the relay nor the
	// staging's time or cancellation had a part in it.
	permanent := errors.As(err, new(deterministicStagingError)) && !relayFailed && ctx.Err() == nil

	c.mu.Lock()
	if err == nil {
		switch current := c.entries[e.key]; current {
		case e:
		case nil:
			// Canceled for want of a session just as it completed: keep it.
			c.entries[e.key] = e
		default:
			err = errors.New("superseded by a later staging")
		}
	}
	var asides []string
	if err == nil {
		// The rename is the staging's commit: a reader of e.dir sees the
		// complete staging or none.
		asides = c.moveAsideLocked(e.dir)
		err = os.Rename(scratch, e.dir)
	}
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrSubtitleStaging, err)
	}
	delete(c.running, e)
	<-c.slots
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
	e.err, e.endedAt, e.ended = err, c.now(), true
	e.permanent = err != nil && permanent
	e.bytes = size
	if err != nil {
		e.bytes = 0
	}
	more, _ := c.evictLocked(0)
	asides = append(asides, more...)
	close(e.done)
	c.mu.Unlock()
	c.removeAll(asides)
	if err == nil {
		_ = syncDir(c.dir)
	}

	elapsed := c.now().Sub(started).Milliseconds()
	if err != nil {
		slog.WarnContext(ctx, "relayed subtitle staging failed", "component", "playback",
			"source", h.Identity(), "track", job.track, "permanent", e.permanent, "elapsed_ms", elapsed,
			"error", nodesource.RedactLoopback(err.Error()))
	} else {
		slog.InfoContext(ctx, "relayed subtitle staged", "component", "playback",
			"source", h.Identity(), "track", job.track, "bytes", size, "elapsed_ms", elapsed)
	}
}

// stageAside writes e's staging into a scratch directory of its own, on a
// handle reopened from h, and syncs it to disk. It returns the scratch
// directory (removed by the caller), the staging's size, and whether the
// relay failed a read of the source.
func (c *RelayedSubtitleStaging) stageAside(ctx context.Context, e *subtitleStaging, h *nodesource.Handle, job stagingJob) (string, int64, bool, error) {
	own, err := h.Reopen(ctx)
	if err != nil {
		return "", 0, true, fmt.Errorf("open the source: %w", err)
	}
	defer func() { _ = own.Close() }()
	job.input = own.Input()
	scratch, err := os.MkdirTemp(c.dir, stagingTempPrefix+e.key+"-")
	if err != nil {
		return "", 0, false, err
	}
	err = c.stage(ctx, job, scratch)
	relayFailed := own.Err() != nil || own.UpstreamFailed()
	if err != nil {
		return scratch, 0, relayFailed, err
	}
	size, ready := verifiedStaging(scratch)
	if !ready {
		return scratch, 0, relayFailed, deterministic(errors.New("the staged subtitle is incomplete"))
	}
	return scratch, size, relayFailed, syncStaging(scratch)
}

// evictLocked makes the cache's stagings fit maxBytes with extra more: it
// moves aside the least recently used unleased completed stagings, and
// drops unleased passing failures past their time. A staging in progress
// counts stagingReserveBytes. It returns the directories moved aside, for
// the caller to remove once it releases the mutex, and whether extra fits.
func (c *RelayedSubtitleStaging) evictLocked(extra int64) ([]string, bool) {
	now := c.now()
	used := int64(len(c.running)) * stagingReserveBytes
	var evictable int64
	for key, e := range c.entries {
		switch {
		case !e.ended:
		case e.err != nil:
			if e.leases == 0 && !e.permanent && now.Sub(e.endedAt) >= stagedSubtitleFailureTTL {
				delete(c.entries, key)
			}
		default:
			used += e.bytes
			if e.leases == 0 {
				evictable += e.bytes
			}
		}
	}
	if extra > 0 && used-evictable+extra > c.maxBytes {
		// Evicting everything it may would still not make room: keep it.
		return nil, false
	}
	var asides []string
	for used+extra > c.maxBytes {
		var oldest *subtitleStaging
		for _, e := range c.entries {
			if e.leases == 0 && e.ended && e.err == nil && (oldest == nil || e.lastUsed.Before(oldest.lastUsed)) {
				oldest = e
			}
		}
		if oldest == nil {
			return asides, false // every staging is in use
		}
		asides = append(asides, c.removeLocked(oldest)...)
		used -= oldest.bytes
	}
	return asides, true
}

// removeLocked drops e from the cache and moves its directory aside.
func (c *RelayedSubtitleStaging) removeLocked(e *subtitleStaging) []string {
	delete(c.entries, e.key)
	return c.moveAsideLocked(e.dir)
}

// moveAsideLocked renames dir, when it exists, to a temporary name in the
// cache, which a later staging under the same key cannot collide with, and
// returns that name for removal outside the mutex.
func (c *RelayedSubtitleStaging) moveAsideLocked(dir string) []string {
	c.asides++
	aside := filepath.Join(c.dir, stagingTempPrefix+"removed-"+strconv.FormatInt(c.now().UnixNano(), 36)+"-"+strconv.Itoa(c.asides))
	err := os.Rename(dir, aside)
	switch {
	case err == nil:
		return []string{aside}
	case errors.Is(err, os.ErrNotExist):
		return nil
	}
	// It cannot be renamed; it must still be gone before the mutex is
	// released, or a later staging's rename could land in it.
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("remove staged subtitle", "component", "playback", "dir", dir, "error", err)
	}
	return nil
}

// removeAll removes directories that left the cache. The caller does not
// hold the cache's mutex.
func (c *RelayedSubtitleStaging) removeAll(dirs []string) {
	for _, dir := range dirs {
		if err := c.removeDir(dir); err != nil {
			slog.Warn("remove staged subtitle", "component", "playback", "dir", dir, "error", err)
		}
	}
}

// Sweep removes the completed stagings no session has used for maxAge, and
// forgets the failures that ended before then, and reports how many stagings
// it removed. A transcode node's orphan sweep calls it.
func (c *RelayedSubtitleStaging) Sweep(maxAge time.Duration) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	cutoff := c.now().Add(-maxAge)
	removed := 0
	var asides []string
	for key, e := range c.entries {
		if e.leases > 0 || !e.ended {
			continue
		}
		switch {
		case e.err != nil && e.endedAt.Before(cutoff):
			delete(c.entries, key)
		case e.err == nil && e.lastUsed.Before(cutoff):
			asides = append(asides, c.removeLocked(e)...)
			removed++
		}
	}
	more, _ := c.evictLocked(0)
	asides = append(asides, more...)
	c.mu.Unlock()
	c.removeAll(asides)
	return removed
}

// stageSubtitle extracts job's track and the source's attached fonts into
// dir: the script streamed to disk, the fonts within their bounds.
func stageSubtitle(ctx context.Context, job stagingJob, dir string) error {
	codec := subtitleFormatASS
	if job.codec == subtitleFormatASS || job.codec == subtitleCodecSSA {
		codec = codecCopyV3
	}
	script, err := os.OpenFile(filepath.Join(dir, stagedSubtitleFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = stagingRun(ctx, job.ffmpeg, nodesource.RestrictArgs([]string{
		ffmpegHideBannerArg, ffmpegNoStdinArg, ffmpegLogLevelArg, ffmpegErrorLogLevel,
		// No seek and no start_at_zero: the events keep the source's
		// timestamps, which the transcode's -copyts frames carry too.
		"-copyts", "-i", job.input,
		ffmpegMapArg, "0:s:" + strconv.Itoa(job.track), "-c:s", codec, "-f", subtitleFormatASS, ffmpegStdout,
	}), script, stagedSubtitleMaxBytes)
	if closeErr := script.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("extract track: %w", err)
	}
	if err := checkASSScript(filepath.Join(dir, stagedSubtitleFile)); err != nil {
		return deterministic(err)
	}
	fonts, err := stagedSubtitleFonts(ctx, job.ffmpeg, job.input)
	if err != nil {
		return err
	}
	fontDir := filepath.Join(dir, stagedSubtitleFontsDir)
	if err := os.Mkdir(fontDir, 0o700); err != nil {
		return err
	}
	for _, font := range fonts {
		if err := os.WriteFile(filepath.Join(fontDir, font.Name), font.Data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// checkASSScript requires the extracted track to start as an ASS script
// does, with its [Script Info] section, after an optional byte order mark
// and blank space.
func checkASSScript(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	text := strings.TrimLeft(strings.TrimPrefix(string(head[:n]), utf8BOM), " \t\r\n")
	if !strings.HasPrefix(text, assScriptHeader) {
		return errors.New("the extracted track is not an ASS script")
	}
	return nil
}

// verifiedStaging reports a complete staging at dir whose content is what
// staging writes, and its size: stagedSubtitleReady, a script that starts as
// an ASS script does, and fonts whose bytes start as a font's. A node checks
// the stagings it adopts this way, as it checks those it writes.
func verifiedStaging(dir string) (int64, bool) {
	size, ready := stagedSubtitleReady(dir)
	if !ready || checkASSScript(filepath.Join(dir, stagedSubtitleFile)) != nil {
		return 0, false
	}
	fontDir := filepath.Join(dir, stagedSubtitleFontsDir)
	fonts, err := os.ReadDir(fontDir)
	if err != nil {
		return 0, false
	}
	for _, font := range fonts {
		head, err := readHead(filepath.Join(fontDir, font.Name()), 4)
		if err != nil || !isFontData(head) {
			return 0, false
		}
	}
	return size, true
}

// readHead reads up to n bytes from the start of the file at path.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, n)
	read, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return head[:read], nil
}

// syncStaging flushes a staging's files and directories to disk, so the
// rename that commits it never names a directory whose content a crash could
// still lose.
func syncStaging(dir string) error {
	fontDir := filepath.Join(dir, stagedSubtitleFontsDir)
	fonts, err := os.ReadDir(fontDir)
	if err != nil {
		return err
	}
	paths := []string{filepath.Join(dir, stagedSubtitleFile)}
	for _, font := range fonts {
		paths = append(paths, filepath.Join(fontDir, font.Name()))
	}
	for _, path := range append(paths, fontDir, dir) {
		if err := syncDir(path); err != nil {
			return err
		}
	}
	return nil
}

// syncDir flushes the file or directory at path to disk.
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// stagedSubtitleReady reports a complete staging at dir, and its size: a
// real directory holding a script within bounds and a fonts directory of
// regular font files within the attachment bounds.
func stagedSubtitleReady(dir string) (int64, bool) {
	if !isRealDir(dir) || !isRealDir(filepath.Join(dir, stagedSubtitleFontsDir)) {
		return 0, false
	}
	info, err := os.Lstat(filepath.Join(dir, stagedSubtitleFile))
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > stagedSubtitleMaxBytes {
		return 0, false
	}
	fonts, err := os.ReadDir(filepath.Join(dir, stagedSubtitleFontsDir))
	if err != nil || len(fonts) > maxSubtitleFontAttachments {
		return 0, false
	}
	var total int64
	for _, font := range fonts {
		info, err := font.Info()
		if err != nil || !info.Mode().IsRegular() || !isSupportedFontExt(filepath.Ext(font.Name())) || info.Size() > maxSubtitleFontBytes-total {
			return 0, false
		}
		total += info.Size()
	}
	return info.Size() + total, true
}

func isRealDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func useStagedSubtitle(opts *TranscodeOpts, dir string) {
	opts.subtitleFilterInputPath = filepath.Join(dir, stagedSubtitleFile)
	opts.subtitleFilterFontsDir = filepath.Join(dir, stagedSubtitleFontsDir)
}

// stagedSubtitleFonts extracts the fonts attached to input, a relayed source.
// It keeps what local libass rendering would not fail on: an attachment with
// no data, one past the count or byte bounds (the first fonts within them are
// kept, as ListAttachedSubtitleFonts does), or one whose bytes do not start
// like a TrueType, OpenType, collection or WOFF font is skipped, not fatal.
// Each kept font is named by its stream index and a supported extension: the
// attachment's own filename, which the source chooses, never becomes a path.
func stagedSubtitleFonts(ctx context.Context, ffmpeg, input string) ([]SubtitleFontAttachment, error) {
	var listing bytes.Buffer
	if err := stagingRun(ctx, ffprobePathFromFFmpeg(ffmpeg), nodesource.RestrictProbeArgs([]string{
		"-v", ffmpegErrorLogLevel, "-select_streams", "t",
		"-show_entries", "stream=index,codec_name,codec_type,extradata_size:stream_tags=filename,mimetype",
		"-of", "json", input,
	}), &listing, stagedSubtitleProbeMaxBytes); err != nil {
		return nil, fmt.Errorf("probe attached fonts: %w", err)
	}
	streams, err := fontAttachmentStreams(listing.Bytes())
	if err != nil {
		return nil, deterministic(err)
	}
	streams = boundedFontStreams(streams)
	if len(streams) == 0 {
		return nil, nil
	}
	var expected int64
	args := []string{ffmpegHideBannerArg, ffmpegNoStdinArg, ffmpegLogLevelArg, ffmpegErrorLogLevel}
	for _, stream := range streams {
		args = append(args, "-dump_attachment:"+strconv.Itoa(stream.Index), ffmpegStdout)
		expected += stream.ExtraDataSize
	}
	// Attachments are in the container header: -t 0 stops before the media.
	args = append(args, "-i", input, ffmpegMapArg, "0:t?", "-c", codecCopyV3, "-t", "0", "-f", "null", "-")
	var dumped bytes.Buffer
	if err := stagingRun(ctx, ffmpeg, nodesource.RestrictArgs(args), &dumped, expected); err != nil {
		return nil, fmt.Errorf("extract attached fonts: %w", err)
	}
	return splitStagedFonts(streams, dumped.Bytes())
}

// fontAttachmentStreams reads ffprobe's attachment listing and keeps the
// fonts, each stream once.
func fontAttachmentStreams(listing []byte) ([]attachmentProbeStream, error) {
	var probed attachmentProbeOutput
	if err := json.Unmarshal(listing, &probed); err != nil {
		return nil, errors.New("unreadable attached font listing")
	}
	var streams []attachmentProbeStream
	seen := make(map[int]bool)
	for _, stream := range probed.Streams {
		if !isFontAttachment(stream) || seen[stream.Index] {
			continue
		}
		seen[stream.Index] = true
		streams = append(streams, stream)
	}
	return streams, nil
}

// boundedFontStreams keeps the fonts to extract: in container order, those
// with data, at most maxSubtitleFontAttachments of them, skipping any that
// would take the total past maxSubtitleFontBytes.
func boundedFontStreams(streams []attachmentProbeStream) []attachmentProbeStream {
	var kept []attachmentProbeStream
	var total int64
	for _, stream := range streams {
		if len(kept) == maxSubtitleFontAttachments {
			break
		}
		if stream.Index < 0 || stream.ExtraDataSize < 1 || stream.ExtraDataSize > maxSubtitleFontBytes-total {
			continue
		}
		total += stream.ExtraDataSize
		kept = append(kept, stream)
	}
	return kept
}

// splitStagedFonts cuts the dumped bytes into the probed fonts, naming each
// by its stream index and dropping any whose leading bytes are not a font's.
// Bytes that differ in length from the probe fail: the cut would be wrong.
func splitStagedFonts(streams []attachmentProbeStream, data []byte) ([]SubtitleFontAttachment, error) {
	var expected int64
	for _, stream := range streams {
		expected += stream.ExtraDataSize
	}
	if int64(len(data)) != expected {
		return nil, deterministic(errors.New("attached fonts differ from their probed sizes"))
	}
	fonts := make([]SubtitleFontAttachment, 0, len(streams))
	offset := 0
	for _, stream := range streams {
		end := offset + int(stream.ExtraDataSize)
		font := data[offset:end:end]
		offset = end
		if !isFontData(font) {
			continue
		}
		fonts = append(fonts, SubtitleFontAttachment{
			StreamIndex: stream.Index,
			Name:        "attachment-" + strconv.Itoa(stream.Index) + stagedFontExt(stream),
			Data:        font,
		})
	}
	return fonts, nil
}

// isFontData reports bytes that start like a TrueType, OpenType, font
// collection or WOFF font.
func isFontData(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "true", "OTTO", "ttcf", "wOFF", "wOF2":
		return true
	}
	return false
}

// stagedFontExt is the extension a staged font is written under: the
// attachment's, when supported, else ".ttf", which libass loads whatever the
// font's actual format.
func stagedFontExt(stream attachmentProbeStream) string {
	if ext := fontAttachmentExt(stream); isSupportedFontExt(ext) {
		return ext
	}
	return ttfFontExt
}

// stagingRun runs a staging process, copying its stdout to w, and fails when
// the process fails or writes more than maxBytes. Either failure, outside a
// canceled ctx, is deterministic: the caller tells a relay failure that made
// the process fail apart.
func stagingRun(ctx context.Context, bin string, args []string, w io.Writer, maxBytes int64) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	n, copyErr := io.Copy(w, io.LimitReader(stdout, maxBytes+1))
	if copyErr != nil || n > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if copyErr != nil {
			return fmt.Errorf("write output: %w", copyErr)
		}
		return deterministic(fmt.Errorf("output exceeds %d bytes", maxBytes))
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tail := truncateStderr(stderr.String()); tail != "" {
			return deterministic(fmt.Errorf("%w (stderr: %s)", err, nodesource.RedactLoopback(tail)))
		}
		return deterministic(err)
	}
	return nil
}
