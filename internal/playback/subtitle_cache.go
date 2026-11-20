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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/models"
)

// SubtitleCache stores complete SUP, VTT, and ASS subtitle extracts on disk
// so repeat selections don't re-run a whole-file ffmpeg demux. Even a small
// text subtitle track can require reading a multi-GB source to completion.
//
// Entries are keyed by the source path, subtitle ordinal, source mtime+size,
// and output format. When the source changes, the lookup key changes and the
// old entry becomes garbage that eviction reclaims. Entry recency for LRU is
// tracked by bumping the cache file's mtime on every hit (portable, unlike
// atime which is often disabled via noatime/relatime mounts).
//
// Concurrency: the first requester of an uncached track streams the extract
// progressively to its client while teeing bytes into a temp file that is
// atomically renamed into the cache on clean ffmpeg exit (and discarded on
// any error, so a partial entry is never served). A request that arrives while
// a fill is already in flight waits for that fill to settle (bounded by its
// own request context) and then serves the committed entry, so a plan-time
// warm is never duplicated by the first client fetch. A request that finds no
// in-flight fill streams independently as before, so a genuinely cold path
// never depends on another viewer's connection.
type SubtitleCache struct {
	// transcodeDir returns the current transcode directory; the cache lives
	// in a subtitle-cache subdirectory beneath it, created lazily. An empty
	// return disables the cache for that call.
	transcodeDir func() string
	source       EmbeddedSubtitleSource
	// maxBytes is the total-size eviction budget for committed entries.
	maxBytes int64
	// extractRetryDelays is the bounded in-request retry schedule for an
	// upstream failure that emitted no bytes. NewSubtitleCache installs the
	// production schedule; tests override it on their own cache instead of
	// mutating package state.
	extractRetryDelays []time.Duration
	// fillWaitTimeout bounds how long a duplicate request waits for an
	// in-flight fill before answering the retryable "still working" shape.
	// NewSubtitleCache installs the production bound; tests override it on
	// their own cache.
	fillWaitTimeout time.Duration
	// windowElapsedBudget overrides subtitleTextWindowElapsedBudget for this
	// cache's request-path text window extracts. Zero means the production
	// bound; tests set a short one (see SetWindowExtractBudgetForTest).
	windowElapsedBudget time.Duration
	// windowWaitTimeout bounds how long a duplicate window request waits for
	// the in-flight extract it joined before answering retryable. Zero means
	// the production bound; tests set a short one (see
	// SetWindowWaitTimeoutForTest).
	windowWaitTimeout time.Duration

	mu       sync.Mutex
	inflight map[string]*SubtitleCacheFill

	// warmMu guards warmGuard, the per-identity failed-warm cooldown. It is
	// separate from mu so admission never contends with in-flight fill
	// bookkeeping.
	warmMu    sync.Mutex
	warmGuard map[string]subtitleWarmGuard

	// warmSem bounds concurrent background warms server-wide (each warm
	// demuxes an entire source file — heavy sequential IO). Acquisition is
	// non-blocking: warms beyond the budget are dropped, not queued; the
	// next windowed miss for that track re-attempts the warm.
	warmSem chan struct{}

	// fontBundleFlight coalesces concurrent font-bundle extractions for the
	// same source so a font fetch stampede runs ffprobe+ffmpeg exactly once.
	fontBundleFlight singleflight.Group

	// windowMu guards the in-memory last-good window cache and the per-window
	// in-flight set. Repeated client requests for the same bounded slice (the
	// normal retry pattern after an upstream flap) must not re-run ffmpeg, and
	// concurrent duplicates must not fan out into parallel extracts.
	windowMu      sync.Mutex
	windowResults map[string]subtitleWindowResult
	windowBytes   int64
	windowFlights map[string]*subtitleWindowFlight

	// pageFlights tracks admitted windowed ("page") extracts per track cache
	// key and warmRuns the full-track warm currently extracting the same
	// track. A page and a warm share one relay pipe; when a page is admitted
	// the warm must yield it, then resume once the page settles. Both maps are
	// guarded by windowMu so the collision check and the cancellation are one
	// atomic step (a page can never miss a warm that is about to register).
	pageFlights map[string]*subtitlePageFlight
	warmRuns    map[string]*subtitleWarmRun

	// failureMu guards failureGuard, the per-identity extraction backoff. An
	// upstream 5xx is transient, but a client retrying every few seconds would
	// otherwise re-open the provider and spawn ffmpeg on every attempt.
	failureMu    sync.Mutex
	failureGuard map[string]subtitleExtractionGuard

	// pageCostMu guards pageReadRates, the read rate last observed for each
	// remote source identity. It prices the container prefix a further bitmap
	// page would read, so a page that would spend its whole client fetch
	// discarding that prefix is refused before the read instead of after it
	// (see subtitle_page_cost.go).
	pageCostMu    sync.Mutex
	pageReadRates map[string]float64

	// pageWaitMu guards pageWaits, one bounded wait per playback session and
	// bitmap track while the track's progressive artifact parses toward the
	// page (see subtitle_page_recovery.go). pageWaitBudget and pageWaitNow are
	// test seams; zero keeps the production budget and wall clock.
	pageWaitMu     sync.Mutex
	pageWaits      map[string]subtitlePageRecovery
	pageWaitBudget time.Duration
	pageWaitNow    func() time.Time

	// fillEndMu guards fillEnds, the last time each track's fill stopped
	// without committing and why. A page whose fill ended is not being moved
	// toward by anything, so the page answers from this record instead of
	// looping on `page_not_ready` until its own budget expires (see the warm
	// goroutine in subtitle_cache.go and subtitle_page_recovery.go).
	fillEndMu sync.Mutex
	fillEnds  map[string]subtitleFillEnd

	// fontFailureMu guards fontFailureRetryAt, the per-font-bundle extraction
	// backoff. A failed font extraction is optional to playback, so callers
	// degrade to an empty pending bundle instead of retrying a flapping
	// provider on every request. fontFailureFallback records the failures that
	// are permanent for this key (subsetting exhausted the only paths to a
	// bundle), so responses keep an explicit system-font fallback marker while
	// the backoff lasts.
	fontFailureMu       sync.Mutex
	fontFailureRetryAt  map[string]time.Time
	fontFailureFallback map[string]string

	// warmTextMu guards warmText, the in-progress full-track ASS warms. A
	// windowed request whose range a warm has already produced is answered
	// from this memory copy instead of demuxing the relay source again.
	warmTextMu sync.Mutex
	warmText   map[string]*subtitleWarmTextProgress

	// demuxBusyWarnMu guards demuxBusyWarnAt, the interval throttle for the
	// relay-shed warning. Shedding a client-facing subtitle read answers a
	// retryable 503; a viewer who keeps retrying can burn a client's retry
	// budget before anyone notices, so each shed is worth a warning, but a
	// saturated source sheds every request in a retry loop. One warning per
	// subtitleDemuxBusyWarnEvery per cache makes the contention visible
	// without flooding the log. The interval and clock are fields so tests can
	// drive them without touching package state.
	demuxBusyWarnMu    sync.Mutex
	demuxBusyWarnAt    time.Time
	demuxBusyWarnEvery time.Duration
	demuxBusyWarnNow   func() time.Time
}

// subtitleFillEnd is one track fill's non-committing end: when it happened and
// why (a reason token, not prose).
type subtitleFillEnd struct {
	at     time.Time
	reason string
}

const (
	subtitleFormatSRT        = "srt"
	subtitleCodecSubRip      = "subrip"
	subtitleCodecSSA         = "ssa"
	subtitleFormatSUP        = "sup"
	subtitleFormatASS        = "ass"
	subtitleMuxerWebVTT      = "webvtt"
	subtitleFormatFontBundle = "fontbundle"
	subtitleCacheDirName     = "subtitle-cache"
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
	// subtitleCacheGenerationBucket bounds the staleness of identity-keyed
	// (remote / virtual / generated) cache entries. The identity already pins
	// the provider result candidate id, so a release that actually rotates
	// produces a different key; for subtitles the bucket is only residual
	// insurance against the same pinned id later serving changed bytes. It was
	// ten minutes, which discarded a warmed ASS/SUP artifact between plays and
	// forced a full ~100s relay demux on the next request. A day keeps a
	// warmed artifact across a viewing session while still bounding that
	// residual risk.
	subtitleCacheGenerationBucket = 24 * time.Hour
	// subtitleDemuxBusyWarnEvery bounds how often one cache warns that it shed
	// a client-facing subtitle read because the relay source was saturated.
	// The shed itself is expected backpressure; the warning exists so repeated
	// multi-viewer contention is visible before clients exhaust their retry
	// budgets, without one log line per retry.
	subtitleDemuxBusyWarnEvery = time.Minute
	// subtitleFontBundleExtractTimeout bounds one detached font-bundle
	// extraction. The extraction runs single-flighted and detached from the
	// request that triggered it, and its successful result is written through
	// to disk. A cold relay carrying a large anime font set (up to 47
	// attachments, each a network open) can take minutes, so the old 60s
	// ceiling discarded work before it could ever be stored — the reason every
	// retry repeated the same failure. Five minutes keeps a hard bound while
	// letting a cold extraction finish; the HTTP path degrades to an empty
	// bundle after its short client wait long before this fires.
	subtitleFontBundleExtractTimeout = 5 * time.Minute
	// subtitleFillWaitRetries bounds how many in-flight fills a single request
	// waits on before falling back to its own uncached stream. A fill that
	// fails without committing leaves no entry; the waiter re-attempts the
	// reservation, so one transient failure still yields a served response.
	// The cap stops a pathological run of failing fills from wedging a request
	// indefinitely when its context has no deadline.
	subtitleFillWaitRetries = 3
	// subtitleFillBusyRetryAfter is the Retry-After handed to a request that
	// joined an in-flight fill and could not get a committed entry from it. It
	// is shorter than the upstream-failure backoff on purpose: the answer is
	// "the extraction already running will land soon", not "the provider is
	// failing".
	subtitleFillBusyRetryAfter = 2 * time.Second
	// subtitlePageCostlyRetryAfter is the Retry-After handed to a page whose
	// extraction was killed for reading too much of the container prefix. It
	// matches the fill-busy delay: the answer is "the progressive artifact is
	// being read, come back for the page", not "the provider failed".
	subtitlePageCostlyRetryAfter = subtitleFillBusyRetryAfter
	// subtitleFillWaitTimeout bounds how long a duplicate request waits on an
	// in-flight fill before answering retryable. A local demux finishes well
	// inside it; a slow provider read that a second request is racing (the
	// production 16s extract) hands that request the retry shape instead of
	// holding its connection open and then stacking a second extraction.
	subtitleFillWaitTimeout = 5 * time.Second
	// subtitleWarmFailureBaseCooldown is the first backoff after a warm for
	// one identity fails or is abandoned. Repeated windowed misses on a
	// source that cannot be warmed (provider error, broken relay,
	// unreadable file) would otherwise each spawn a full-track remote read —
	// the two warm slots bound concurrency, not total bytes or cluster load.
	// A failed identity is suppressed for this long before the next attempt.
	subtitleWarmFailureBaseCooldown = time.Minute
	// subtitleWarmFailureMaxCooldown caps the exponential backoff so a
	// permanently broken source is retried eventually instead of never (the
	// generation bucket, not the cooldown, is what re-keys it after a real
	// source rotation).
	subtitleWarmFailureMaxCooldown = 30 * time.Minute
	// subtitleWarmGuardMaxEntries caps the per-identity failed-warm cooldown
	// map. A live entry is an identity still inside its cooldown; without a cap
	// a long-lived server accumulates one entry for every identity that ever
	// failed to warm, including identities whose cooldown lapsed long ago.
	// 4096 is far above the number of distinct tracks that can fail inside the
	// 30m maximum cooldown, so eviction only fires under pathological churn.
	// It does not change when a retained entry suppresses a warm: an evicted
	// live entry simply lets the next attempt for that identity run sooner
	// than its backoff would have allowed.
	subtitleWarmGuardMaxEntries = 4096
	// subtitleWarmPageYieldWait bounds how long a warm waits for an admitted
	// page extract on the same track to settle. A page is what the viewer is
	// waiting on, so the warm (an opportunistic full-track read) yields the
	// shared relay pipe; waiting past this hands its warm slot back instead of
	// pinning it behind a long page. The next windowed miss re-attempts.
	subtitleWarmPageYieldWait = 20 * time.Second
	// subtitleWarmPageYieldAttempts caps how many page interruptions one warm
	// tolerates before it abandons. Each restart repeats work already done, so
	// a page storm that never leaves a gap is better served by the next miss
	// than by a warm that keeps demuxing from the start.
	subtitleWarmPageYieldAttempts = 4
	// subtitleFillEndsMaxEntries caps the non-committing fill-end record. One
	// entry per track is tiny; the cap only keeps a long-lived node from
	// retaining one per track it ever filled.
	subtitleFillEndsMaxEntries = 1024
	// subtitleWarmRequestShedQuietWindow keeps a new warm off a relay source
	// whose request-path admission recently shed a client read. The warm is
	// opportunistic: adding a full-track demux to a pipe that is already
	// refusing the window the viewer waits on makes both slower. Once the
	// source stops shedding for this long, the next windowed miss re-attempts.
	subtitleWarmRequestShedQuietWindow = 15 * time.Second
	// subtitleWindowResultTTL bounds how long a successful bounded window stays
	// servable as the last-good copy. A hit refreshes the deadline, so a client
	// that keeps retrying the same window through an upstream outage keeps its
	// cached cues without pinning memory indefinitely.
	subtitleWindowResultTTL = 30 * time.Minute
	// subtitleWindowResultMaxBytes caps the in-memory last-good window cache.
	// Windows are small text/ASS slices; the budget holds many of them.
	subtitleWindowResultMaxBytes = 32 << 20
	// subtitleWindowResultMaxEntryBytes skips caching a single oversized slice
	// rather than letting one response evict the rest of the budget.
	subtitleWindowResultMaxEntryBytes = 8 << 20
	// subtitleWindowResultMaxEntries caps the number of windows kept regardless
	// of their combined size (many tiny windows could otherwise accumulate).
	subtitleWindowResultMaxEntries = 128
	// subtitleWindowWaitTimeout bounds how long a duplicate window request waits
	// for the in-flight extract it joined before answering retryable. A local or
	// small-source window completes well inside it; a stuck remote extract does
	// not hold the duplicate request open.
	subtitleWindowWaitTimeout = 5 * time.Second
	// subtitleWindowWaitRetries bounds how many consecutive in-flight window
	// flights a request joins. A failure with no cached result lets it become
	// the leader; the cap stops a pathological run of failed leaders from
	// wedging one request indefinitely.
	subtitleWindowWaitRetries = 2
	// subtitleWindowExtractTimeout bounds a coalesced window extraction after
	// its leader disconnects. The extract runs detached from the leader's
	// request context so waiting followers can still receive its committed
	// result, but it cannot run forever.
	subtitleWindowExtractTimeout = 2 * time.Minute
	// subtitleExtractionFailureBaseCooldown is the first backoff after a
	// retryable upstream extraction failure. It is deliberately longer than the
	// typical client retry cadence (a few seconds) so the provider is not
	// re-opened on every retry.
	subtitleExtractionFailureBaseCooldown = 10 * time.Second
	// subtitleExtractionFailureMaxCooldown caps the exponential backoff so a
	// permanently dead source is retried eventually rather than never.
	subtitleExtractionFailureMaxCooldown = 60 * time.Second
	// subtitleExtractionFailureHistoryTTL bounds how long a lapsed failure
	// streak is retained. The streak outlives retryAt so every failure in a
	// sustained outage keeps escalating the cooldown (10→20→40→60) instead of
	// restarting at 10s, while an identity that has been quiet this long
	// forgets its history and starts fresh.
	subtitleExtractionFailureHistoryTTL = 30 * time.Minute
	// subtitleExtractionFailureMaxEntries caps the extraction backoff map. The
	// map retains lapsed streaks (bounded by
	// subtitleExtractionFailureHistoryTTL) until pressure evicts the least
	// recently failed entry, so the cap only limits pathological churn.
	subtitleExtractionFailureMaxEntries = 4096
	// subtitleFontBundleFailureCooldown suppresses repeated font-bundle
	// extractions after a transient/upstream failure. Font bundles are optional
	// to playback, so a client retry inside the window gets the same pending
	// empty bundle instead of spawning another ffprobe+ffmpeg.
	subtitleFontBundleFailureCooldown = 15 * time.Second
	// subtitleFontBundleFailureMaxEntries caps the font-bundle failure map.
	subtitleFontBundleFailureMaxEntries = 512
)

// errSubtitleFontBundleFlightSkipped reports a font flight dropped because the
// relay was already at its font-demux ceiling. It is expected backpressure, not
// a failed extraction, and is reported separately from a timeout.
var errSubtitleFontBundleFlightSkipped = errors.New("subtitle font bundle extraction skipped: relay demux busy")

// SubtitleFontFallbackSubsetUnavailable is the response marker a font-bundle
// failure carries when the client must use system fonts: the complete pack is
// past the ceiling and script-driven subsetting could not produce one (an
// unreadable script, too many tracks, too many decoded bytes, or no referenced
// attachment matched). It is not a transient pending state.
const SubtitleFontFallbackSubsetUnavailable = "subset_unavailable"

// subtitleExtractRetryDelays returns the bounded in-request retry schedule for
// an extraction that failed on an upstream 5xx before emitting any bytes. Each
// delay is the pause before the next attempt; the final count is small because
// the provider outage is usually longer than one request. It returns a fresh
// slice so no caller can mutate shared package state; tests override the
// schedule on their own cache instance.
func subtitleExtractRetryDelays() []time.Duration {
	return []time.Duration{200 * time.Millisecond, 500 * time.Millisecond}
}

// SubtitleExtractionRetryError reports that extraction is deliberately paused
// after a retryable upstream failure. No response bytes have been written, so
// HTTP callers answer a retryable 503 with Retry-After. It is returned by
// ServeExtractWithResult while a per-identity backoff is active.
type SubtitleExtractionRetryError struct {
	RetryAfter time.Duration
	// Reason, when set, replaces the retryable response's default problem code
	// with a more specific one (SubtitlePageTooCostlyReason). It is additive:
	// the status, the Retry-After shape and the client's retry loop are
	// unchanged.
	Reason string
	// CoveredUntil, when positive, is the source time, in seconds, that the
	// track's progressive bitmap artifact has parsed. The retryable response
	// advertises it as the covered range so a client can tell how far the
	// artifact it will be served from has reached. Zero means nothing is
	// covered and no coverage is advertised.
	CoveredUntil float64
	// WindowBudgetExceeded reports that the retryable answer came from a
	// request-path text window killed at its elapsed budget rather than from a
	// provider failure. The source is reachable; this page just costs more
	// than one request may spend on it, so a caller may start the detached
	// warm and have the retry served from a committed slice.
	WindowBudgetExceeded bool
	// PageRecoveryDue reports that this is the first refusal for this bitmap
	// page, so the caller may start the one recovery fill it is allowed. Later
	// refusals keep the fill that already owns the track (see
	// subtitle_page_recovery.go).
	PageRecoveryDue bool
}

func (e *SubtitleExtractionRetryError) Error() string {
	return fmt.Sprintf("subtitle extraction retry after %s", e.RetryAfter)
}

// subtitleWindowResult is one successfully extracted bounded window kept as the
// last-good copy for exact repeats and for shifted requests inside its range.
type subtitleWindowResult struct {
	data     []byte
	storedAt time.Time
	// base is the source identity half of the window key (everything before the
	// start/end/open-ended suffix). A covering lookup compares it so a window
	// from another track, format, or source generation can never answer.
	base string
	// startSeconds, endSeconds, and openEnded are the source-time range this
	// slice actually covers. They mirror the extract options that produced it,
	// not the range a later request asked for.
	startSeconds float64
	endSeconds   float64
	openEnded    bool
	// warmPrefix is non-nil when data was rendered by an in-progress ASS warm's
	// prefix rather than extracted from a source or committed artifact. Such a
	// slice is only as complete as that layer's event-ordering guarantee, so
	// disabling the layer evicts these entries and lets the same window fall
	// through to the extraction path.
	warmPrefix *subtitleWarmTextProgress
}

// covers reports whether this slice's source-time range contains the requested
// range. An open-ended request (start to EOF) is only covered by an open-ended
// slice; a bounded request is covered by an open-ended slice that starts at or
// before it.
func (e subtitleWindowResult) covers(startSeconds, endSeconds float64, openEnded bool) bool {
	const epsilon = 1e-6
	if e.startSeconds > startSeconds+epsilon {
		return false
	}
	if openEnded {
		return e.openEnded
	}
	if e.openEnded {
		return true
	}
	return e.endSeconds >= endSeconds-epsilon
}

// spanSeconds is the coverage width used to prefer the tightest covering slice.
func (e subtitleWindowResult) spanSeconds() float64 {
	if e.openEnded {
		return math.Inf(1)
	}
	return e.endSeconds - e.startSeconds
}

// subtitleWindowFlight is a per-window in-flight marker; done closes when the
// leading request finishes (success, failure, or cancellation). It also records
// the range being extracted so a request covered by a wider in-flight window
// (the recovery warm a budget breach starts) waits for it instead of starting a
// second relay read for a slice that is already being produced.
type subtitleWindowFlight struct {
	done      chan struct{}
	base      string
	start     float64
	end       float64
	openEnded bool
	// refusalReason names the answer that started this extract when it is the
	// recovery a refused request is waiting on (see
	// StreamExtractOpts.WaitingRefusalReason). A request that joins the flight
	// and waits past its bound answers with this reason instead of a generic
	// source failure, so the client keeps the state the first refusal gave it.
	refusalReason string
}

// subtitlePageFlight is a refcounted marker for the windowed extracts admitted
// on one track. done closes when the last page releases, which is the signal a
// yielded full-track warm waits on before it resumes reading.
type subtitlePageFlight struct {
	refs int
	done chan struct{}
}

// subtitleWarmRun is the cancel handle a full-track warm publishes while it is
// reading the relay source for a track, so an admitted page on the same track
// can preempt it. cancel is called with ErrSubtitleWarmCanceled, which the warm
// treats as an intentional yield rather than a source failure.
type subtitleWarmRun struct {
	cancel context.CancelCauseFunc
}

// subtitleExtractionGuard tracks consecutive retryable extraction failures for
// one identity, the earliest time the next attempt may run, and when the
// streak last grew. The failures count deliberately outlives retryAt so a
// sustained outage keeps escalating the cooldown; only a success (or the
// history horizon) clears it.
type subtitleExtractionGuard struct {
	failures    int
	retryAt     time.Time
	lastFailure time.Time
	// reason is the problem code the failure that armed retryAt answered with,
	// when it is a cost refusal the client should keep honoring rather than a
	// source verdict (a window killed at its budget). A retry that lands inside
	// the backoff answers the same code instead of degrading to a generic
	// source failure. Empty keeps the generic retryable shape.
	reason string
}

// subtitleWarmGuard is the per-identity warm admission state: how many
// consecutive warms failed and the earliest time the next one may start.
type subtitleWarmGuard struct {
	failures int
	retryAt  time.Time
}

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
		transcodeDir:       transcodeDir,
		maxBytes:           defaultSubtitleCacheMaxBytes,
		extractRetryDelays: subtitleExtractRetryDelays(),
		fillWaitTimeout:    subtitleFillWaitTimeout,
		inflight:           make(map[string]*SubtitleCacheFill),
		pageWaits:          make(map[string]subtitlePageRecovery),
		warmGuard:          make(map[string]subtitleWarmGuard),
		warmSem:            make(chan struct{}, subtitleCacheWarmSlots),
	}
}

// ServeExtract serves an embedded subtitle using the cache for complete
// tracks. Explicit text windows are not committed to disk, but each successful
// slice is kept in a bounded in-memory last-good cache keyed by the exact
// window, so a client retry after an upstream failure is served without
// re-running ffmpeg; SUP retains its progressive window and background-warming
// behavior in ServeSUPExtract. Only one request fills a complete track; a
// request that arrives while a fill is in flight waits for it to commit and
// serves the cached entry (a failed fill falls back to a fresh attempt, then to
// an uncached stream). Retryable upstream failures are retried a bounded number
// of times in-request and then put the identity in a short exponential backoff.
//
// It is ServeExtractWithResult without the serve report. Callers that need to
// know whether a committed artifact was served — for example to decide whether
// track identity still needs validation — use ServeExtractWithResult.
func (c *SubtitleCache) ServeExtract(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SUPExtractFunc) error {
	_, err := c.ServeExtractWithResult(w, r, opts, extract)
	return err
}

// ServeExtractResult reports how a serve was satisfied.
type ServeExtractResult struct {
	// ServedCommittedArtifact is true when the response body came from the
	// committed full-track cache artifact resolved for the request identity:
	// served directly for a whole-track request, or used as the input of a
	// windowed extract. False means the response was produced by a fresh
	// extract of the source or a live fill.
	//
	// A caller must not treat cache presence alone as proof of track
	// identity. It should resolve the artifact with ResolveCommittedTextEntry,
	// skip validation only when that returns a token, and pass the token back
	// through StreamExtractOpts.PinnedTextArtifact. If the pinned artifact is
	// gone by serve time this method returns ErrCommittedTextArtifactGone
	// instead of silently falling back to an unvalidated source extract.
	ServedCommittedArtifact bool
}

// ErrCommittedTextArtifactGone is returned by ServeExtractWithResult when the
// caller pinned a resolved committed text artifact (PinnedTextArtifact) but
// the artifact no longer exists by serve time. No response is written, so the
// caller can revalidate the live track identity and retry rather than stream
// an unvalidated source extract.
var ErrCommittedTextArtifactGone = errors.New("committed subtitle artifact no longer available")

func (c *SubtitleCache) ServeExtractWithResult(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SUPExtractFunc) (ServeExtractResult, error) {
	if err := r.Context().Err(); err != nil {
		return ServeExtractResult{}, err
	}
	start := time.Now()
	// The page-flight key must stay on the source identity: a windowed text
	// serve may rewrite opts.InputPath to a committed artifact below, and that
	// path is not the identity the full-track warm reads.
	sourceOpts := opts
	// Full demuxes can outlive the API listener's absolute write timeout.
	// Reuse the media-stream deadline so progress continues while stalled
	// connections still have a bounded write lifetime.
	w = httpstream.NewRollingDeadlineWriter(w)
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	// Resolve indexed/kept subtitle inputs before invoking the ordinary
	// extractor. This preserves the source lifecycle (including index-only
	// refusal and kept-copy fallback) for both full-track and windowed serves.
	extract = c.sourced(extract)
	if format == subtitleFormatSUP {
		return ServeExtractResult{}, c.ServeSUPExtract(w, r, opts, extract)
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	if format == subtitleFormatASS {
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	}
	var result ServeExtractResult
	// Compute the last-good window key before any rewrite of InputPath (a
	// windowed extract may be redirected to a committed artifact below, whose
	// path/mtime is not the source identity the window is cached under).
	windowKey := ""
	warmTextKey := ""
	if opts.windowIntent() {
		windowKey, _ = c.windowResultKey(opts, format)
		if format == subtitleFormatASS {
			warmTextKey, _ = c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
		}
	}
	windowLeader := false
	if !opts.windowIntent() {
		if c.serveCached(w, r, opts, format) {
			result.ServedCommittedArtifact = true
			return result, nil
		}
		// A cached artifact is the best last-good copy; only consult the
		// failure backoff when there is nothing to serve.
		if retryAfter, cooling := c.extractionCoolingDownFor(opts, format); cooling {
			return result, &SubtitleExtractionRetryError{RetryAfter: retryAfter}
		}
		w.Header().Set("Cache-Control", "no-store")
		counter := &subtitleCountingWriter{w: w}
		// This serve is a heavy relay read: admit it per relay source in its own
		// class so it does not compete with the same session's other reads for a
		// single slow pipe. The response is still uncommitted here, so a
		// saturated budget answers the existing retryable shape instead of
		// stacking another demux on it.
		admittedCtx, releaseRelayDemux, admitErr := acquireRelayDemuxRead(relayDemuxAdmissionContext(r.Context(), opts), opts.InputPath)
		if admitErr != nil {
			c.warnDemuxBusy(r.Context(), opts, format)
			return result, &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter}
		}
		defer releaseRelayDemux()
		warmOverlap := c.warmInFlight(opts, format)
		overlapStart := time.Now()
		recordSubtitleServeSource(subtitleServeSourceExtract)
		err := c.streamFullTrack(admittedCtx, counter, opts, format, extract)
		if warmOverlap {
			recordSubtitleWarmPageOverlap(format, time.Since(overlapStart))
		}
		outcome := subtitleTraceOutcome(r.Context(), err)
		level := slog.LevelInfo
		if outcome != SubtitleTraceOutcomeSuccess {
			level = slog.LevelWarn
		}
		slog.Log(r.Context(), level, "subtitle extract served",
			subtitleTraceAttrs(r.Context(), opts.SourceCodec, subtitlePhaseForOutput(opts.SourceCodec, opts.TargetFormat),
				counter.bytes, outcome, time.Since(start),
				"track", opts.TrackIndex, "format", format)...)
		return result, err
	} else {
		// Windowed text fetches are position-dependent slices. A committed
		// full-track entry is still preferred (the -ss scan reads kilobytes
		// instead of re-demuxing a multi-GB remote source), and the exact
		// window itself is kept in memory as the last-good copy so a client
		// retry after an upstream flap never re-runs ffmpeg for the same
		// bytes. The committed-entry lookup always runs: virtual relay sources
		// disable only the background warm (a detached fill must not outlive
		// the request-scoped relay registration) while an already-populated
		// entry must still be served instead of re-demuxing the source.
		//
		// A pinned artifact (from ResolveCommittedTextEntry) is used by its
		// exact path rather than re-resolved, so a generation-bucket rollover
		// between the caller's identity check and this serve cannot turn a
		// validated artifact into a miss. If the pinned file has been
		// evicted, fail before the response is committed so the caller can
		// revalidate and retry.
		artifact, ok := c.resolveCommittedTextArtifact(opts)
		if ok {
			if _, statErr := os.Stat(artifact.path); statErr == nil {
				slog.DebugContext(r.Context(), "windowed text subtitle extract using cached full track",
					"input", opts.InputPath, "track", opts.TrackIndex, "cache_entry", artifact.path)
				opts.InputPath = artifact.path
				opts.InputIsExtractedText = artifact.format
				result.ServedCommittedArtifact = true
			} else if opts.PinnedTextArtifact != nil {
				// The caller validated identity against this exact artifact
				// and it is now gone. Falling back to the source here would
				// serve an unvalidated ordinal, so surface the miss instead.
				return ServeExtractResult{}, ErrCommittedTextArtifactGone
			}
		}
		if !result.ServedCommittedArtifact {
			if served, sourceErr := c.serveWindowFromSource(r.Context(), w, opts, extract); served {
				return result, sourceErr
			}
		}
		// The exact window cache is keyed on the source identity computed
		// before the committed-artifact rewrite, so it also covers windows
		// extracted from that artifact.
		if windowKey != "" {
			served, serveErr := c.serveWindowResult(w, r, opts, format, windowKey)
			if serveErr != nil {
				// The 200 and part of the slice are already on the wire;
				// serving or extracting again would corrupt the response.
				return result, serveErr
			}
			if served {
				if !result.ServedCommittedArtifact && !opts.DisableBackgroundWarm {
					c.WarmTrackInBackground(opts, extract, subtitleTraceRequestID(r.Context()))
				}
				return result, nil
			}
		}
		// An in-progress full-track ASS warm may already have produced this
		// window's range. Serving it here avoids a second relay demux and
		// leaves the warm's bandwidth untouched, which is what lets the
		// first commit land quickly instead of competing with every window.
		if warmTextKey != "" && !result.ServedCommittedArtifact {
			if windowed, start, end, openEnded := opts.WindowCoverage(); windowed && !openEnded {
				if progress, data, ok := c.warmTextWindow(warmTextKey, start, end); ok {
					if windowKey != "" {
						// Record it as prefix-derived. The slice is only
						// complete while the warm's event ordering holds; an
						// ordering failure evicts it so a repeat of this exact
						// window extracts instead of replaying stale bytes.
						c.storeWindowResultFrom(windowKey, opts, data, progress)
					}
					w.Header().Set("Cache-Control", "no-store")
					w.WriteHeader(http.StatusOK)
					n, writeErr := w.Write(data)
					if writeErr == nil && n != len(data) {
						writeErr = io.ErrShortWrite
					}
					recordSubtitleServeSource(subtitleServeWarmPrefix)
					logWarmTextServe(r, opts, n, start, end)
					return result, writeErr
				}
			}
		}
		if !result.ServedCommittedArtifact {
			// No last-good copy: suppress a provider retry that is known to be
			// inside its backoff window. An artifact-backed extraction reads a
			// local file, so a previous source failure does not apply to it. A
			// detached prefetch is not a client retry and is not paced by this
			// backoff: it is what fills the window cache that the client's next
			// attempt reads, so blocking it here would leave the page
			// unservable — notably after a budget breach, which arms this same
			// backoff on the identity the prefetch needs.
			if !opts.DetachedRelayDemux {
				if retryAfter, reason, cooling := c.extractionCoolingDownReasonFor(opts, format); cooling {
					return result, &SubtitleExtractionRetryError{RetryAfter: retryAfter, Reason: reason}
				}
			}
			if !opts.DisableBackgroundWarm {
				c.WarmTrackInBackground(opts, extract, subtitleTraceRequestID(r.Context()))
			}
		}
		// Coalesce identical in-flight windows: a duplicate request waits for
		// the leader and serves the copy it stores instead of spawning a
		// second ffmpeg. A failed leader leaves either a cached result or a
		// failure backoff behind, both re-checked after the wait.
		if windowKey != "" {
			for attempt := 0; attempt < subtitleWindowWaitRetries; attempt++ {
				leader, wait, refusalReason := c.beginWindowFlight(windowKey, opts)
				if leader {
					windowLeader = true
					break
				}
				if err := c.waitForWindowFlight(r.Context(), wait, refusalReason); err != nil {
					return result, err
				}
				served, serveErr := c.serveWindowResult(w, r, opts, format, windowKey)
				if serveErr != nil {
					return result, serveErr
				}
				if served {
					return result, nil
				}
				if !result.ServedCommittedArtifact && !opts.DetachedRelayDemux {
					if retryAfter, reason, cooling := c.extractionCoolingDownReasonFor(opts, format); cooling {
						return result, &SubtitleExtractionRetryError{RetryAfter: retryAfter, Reason: reason}
					}
				}
			}
		}
	}
	if windowLeader {
		defer c.endWindowFlight(windowKey)
		// A previous leader may have stored its slice between our lookup and
		// our successful reservation; serve that instead of extracting again.
		served, serveErr := c.serveWindowResult(w, r, opts, format, windowKey)
		if serveErr != nil {
			return result, serveErr
		}
		if served {
			return result, nil
		}
	}

	// This serve is about to become a heavy relay read: the request path's
	// window extract, or the upgrade probe's detached full-track fill. Admit it
	// per relay source in its own class so it does not compete with the same
	// session's other reads for a single slow pipe. The response is still
	// uncommitted here, so a saturated budget answers the existing retryable
	// shape instead of stacking another demux on it.
	admittedCtx, releaseRelayDemux, admitErr := acquireRelayDemuxRead(relayDemuxAdmissionContext(r.Context(), opts), opts.InputPath)
	if admitErr != nil {
		c.warnDemuxBusy(r.Context(), opts, format)
		return result, &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter}
	}
	defer releaseRelayDemux()

	// A page extract that reads the source contends with a full-track warm for
	// the same track on the shared relay pipe. Register it so the warm yields;
	// an artifact-backed page reads only local cache, so it has nothing to
	// yield.
	if !result.ServedCommittedArtifact {
		endPage := c.beginPageFlight(sourceOpts, format)
		defer endPage()
	}

	w.Header().Set("Cache-Control", "no-store")
	// A windowed text slice is held until its extract finishes rather than
	// committed byte by byte. A slice cut short by the extract deadline or a
	// canceled request must not reach the client as a 200 carrying a header and
	// half a cue: a partial body is indistinguishable from a complete window and
	// cannot be retried. Holding keeps the response uncommitted, so the caller
	// answers the retryable shape instead. The hold is bounded by the same spill
	// limit the packet conversion uses; a window that outgrows it streams, which
	// no real 120 s slice does.
	hold := newSubtitleResponseHold(w, subtitleSpillBytes)
	counter := &subtitleCountingWriter{w: hold}
	var writer io.Writer = counter
	// A windowed text response waits for its first cue before committing a
	// status. FFmpeg writes its muxer header before the first packet, so a
	// source read that fails afterwards would otherwise leave a browser with a
	// successful 200 whose body is only "WEBVTT\n". The handler can answer
	// retryably only while nothing has been written.
	var gate *subtitleCueGate
	if format == SubtitleFormatVTTV3 || format == subtitleFormatASS {
		gate = newSubtitleCueGate(counter, format)
		writer = gate
	}
	var capture *subtitleWindowCapture
	if windowLeader {
		// Keep the successful slice as the last-good copy. The capture bound is
		// per response, so a huge window is streamed but not retained.
		capture = &subtitleWindowCapture{dst: writer, remaining: subtitleWindowResultMaxEntryBytes}
		writer = capture
	}
	opts.Writer = writer
	// A coalesced extraction belongs to the flight, not only to the leader
	// request. Detach it from the leader's cancellation (bounded by the
	// timeout) so a leader disconnect does not throw away work that waiters
	// can still consume from the last-good cache. The window capture already
	// swallows write failures, so the detached extract keeps producing after
	// the response writer goes away.
	extractCtx, cancelExtract, windowBudget := c.windowExtractContext(admittedCtx, opts, windowLeader)
	defer cancelExtract()
	warmOverlap := c.warmInFlight(opts, format)
	overlapStart := time.Now()
	recordSubtitleServeSource(subtitleServeSourceExtract)
	heldBytes := func() int { return 0 }
	if gate != nil {
		heldBytes = gate.heldBytes
	}
	err := c.runExtract(extractCtx, opts, extract, counter, heldBytes)
	if warmOverlap {
		recordSubtitleWarmPageOverlap(format, time.Since(overlapStart))
	}
	// A clean window with no dialog is a complete empty document; a failed
	// extract drops the held header so nothing commits a 200 for it.
	err = gate.finish(err)
	if err != nil {
		// Nothing reached the client while the slice was held, so the held bytes
		// are dropped: a truncated slice is not a window. A slice that spilled
		// past the hold is already on the wire; the caller aborts it exactly as
		// before. delivered is read before the hold is dropped: once the slice
		// spilled, its bytes are on the wire and nothing can take them back.
		delivered := hold.delivered()
		hold.discard()
		switch {
		case windowBudget > 0 && windowExtractBounded(extractCtx):
			// The request-path text window ran out its elapsed budget. This is
			// checked before the generic deadline so a slow source gets the
			// budget's own retryable shape and its cooldown.
			err = c.windowBudgetBreachError(r.Context(), opts, format, windowBudget, counter.bytes, delivered, time.Since(start))
		case r.Context().Err() == nil && errors.Is(err, context.DeadlineExceeded):
			// The window ran out its own extract deadline. That is a slow
			// source, not a broken extraction: answer retryable so the client
			// backs off instead of spending its terminal budget on a 500.
			err = &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter}
		case r.Context().Err() == nil && IsSubtitleUpstreamError(err):
			c.noteExtractionFailure(opts, format)
		}
	} else {
		// The extract completed. Commit the held slice now.
		if finishErr := hold.finish(); finishErr != nil {
			return result, finishErr
		}
		c.noteExtractionSuccess(opts, format)
		c.storeCompletedWindow(r.Context(), windowKey, opts, format, capture)
	}
	outcome := subtitleTraceOutcome(r.Context(), err)
	level := slog.LevelInfo
	if outcome != SubtitleTraceOutcomeSuccess {
		level = slog.LevelWarn
	}
	slog.Log(r.Context(), level, "subtitle extract served",
		subtitleTraceAttrs(r.Context(), opts.SourceCodec, subtitlePhaseForOutput(opts.SourceCodec, opts.TargetFormat),
			counter.bytes, outcome, time.Since(start),
			"track", opts.TrackIndex, "format", format)...)
	return result, err
}

// windowExtractContext bounds one window extract. A coalesced leader is
// detached from its request, because its result is still useful to the waiters
// that joined it, and bounded by the coalesced-window timeout. A request-path
// text window the viewer is waiting on is bounded by the request-path budget
// instead, which is shorter. The returned budget is 0 unless this extract is
// bounded by the request-path budget.
func (c *SubtitleCache) windowExtractContext(admittedCtx context.Context, opts StreamExtractOpts, leader bool) (context.Context, context.CancelFunc, time.Duration) {
	budget := c.textWindowExtractBudget(opts)
	if !leader && budget <= 0 {
		return admittedCtx, func() {}, 0
	}
	base := admittedCtx
	if leader {
		base = context.WithoutCancel(admittedCtx)
	}
	if budget > 0 && budget < subtitleWindowExtractTimeout {
		ctx, cancel := context.WithTimeoutCause(base, budget, errSubtitleWindowBudget)
		return ctx, cancel, budget
	}
	ctx, cancel := context.WithTimeout(base, subtitleWindowExtractTimeout)
	return ctx, cancel, 0
}

// windowBudgetBreachError turns a text window killed at its budget into the
// retryable shape the client already backs off from: the existing
// per-identity cooldown is armed so a retry does not repeat the read, and the
// caller answers 503 with Retry-After. The handler also starts the covering
// recovery warm for it (WindowBudgetExceeded), so the retry is served from the
// slice that warm produces instead of paying the same source read again.
//
// The answer is the same whether or not the held slice had spilled onto the
// wire. The window is held until it completes, so a real text window is never
// delivered partially; if a pathological slice did spill, the caller sees a
// committed response and aborts it rather than completing it as a 200, which is
// the one thing that must never happen. Returning the raw extract error there
// used to skip both the recovery warm and the reason.
func (c *SubtitleCache) windowBudgetBreachError(ctx context.Context, opts StreamExtractOpts, format string, budget time.Duration, written int64, delivered bool, elapsed time.Duration) error {
	recordSubtitleWindowBudgetKill(format)
	c.noteExtractionFailure(opts, format)
	// The retry that lands inside this backoff must keep answering the cost
	// refusal: a generic source failure there told the client the provider was
	// broken and dropped the state the budget answer carries.
	c.noteExtractionRefusalReason(opts, format, SubtitleWindowBudgetExceededReason)
	if delivered {
		slog.WarnContext(ctx, "subtitle window extract killed at its budget after bytes reached the wire",
			"component", "playback", "input", opts.InputPath, "track", opts.TrackIndex,
			"format", format, "budget", budget, "bytes", written, "elapsed", elapsed)
	} else if written > 0 {
		slog.WarnContext(ctx, "subtitle window extract killed at its budget after writing bytes",
			"component", "playback", "input", opts.InputPath, "track", opts.TrackIndex,
			"format", format, "budget", budget, "bytes", written, "elapsed", elapsed)
	} else {
		slog.WarnContext(ctx, "subtitle window extract killed at its budget",
			"component", "playback", "input", opts.InputPath, "track", opts.TrackIndex,
			"format", format, "budget", budget, "elapsed", elapsed)
	}
	retryAfter := subtitleExtractionFailureBaseCooldown
	if remaining, cooling := c.extractionCoolingDownFor(opts, format); cooling {
		retryAfter = remaining
	}
	return &SubtitleExtractionRetryError{
		RetryAfter:           retryAfter,
		Reason:               SubtitleWindowBudgetExceededReason,
		WindowBudgetExceeded: true,
	}
}

// runExtract invokes the extractor, retrying a bounded number of times when an
// upstream 5xx failed before any bytes reached the client. Retrying after bytes
// have been written would duplicate or corrupt the response, and a non-upstream
// failure is not expected to recover within one request.
//
// heldBytes counts output buffered by a response gate that has not reached the
// client yet. A failed attempt that filled that buffer must not be retried:
// the next attempt would append a second header to the same response.
func (c *SubtitleCache) runExtract(ctx context.Context, opts StreamExtractOpts, extract SUPExtractFunc, counter *subtitleCountingWriter, heldBytes func() int) error {
	if heldBytes == nil {
		heldBytes = func() int { return 0 }
	}
	delays := c.retryDelays()
	var err error
	for attempt := 0; ; attempt++ {
		err = extract(ctx, opts)
		if err == nil || ctx.Err() != nil || counter.bytes > 0 || heldBytes() > 0 ||
			!IsSubtitleUpstreamError(err) || attempt >= len(delays) {
			return err
		}
		delay := delays[attempt]
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		slog.DebugContext(ctx, "retrying subtitle extraction after upstream failure",
			"component", "playback", "track", opts.TrackIndex, "attempt", attempt+2, "error", err)
	}
}

// retryDelays returns this cache's in-request retry schedule, falling back to
// the production schedule for a disabled (nil) cache.
func (c *SubtitleCache) retryDelays() []time.Duration {
	if c != nil && c.extractRetryDelays != nil {
		return c.extractRetryDelays
	}
	return subtitleExtractRetryDelays()
}

// subtitleWindowCapture tees the response into a bounded buffer while still
// writing every byte to the client. Once the bound is reached it keeps
// forwarding but stops retaining, so an oversized window is never cached.
type subtitleWindowCapture struct {
	dst            io.Writer
	buf            bytes.Buffer
	remaining      int64
	bytes          int64
	deliveryFailed bool
	// truncated marks a response that exceeded the capture bound. The buffer
	// then holds a prefix, so it must never be stored as a complete slice.
	truncated bool
}

func (w *subtitleWindowCapture) Write(p []byte) (int, error) {
	if !w.deliveryFailed {
		n, err := w.dst.Write(p)
		if err != nil || n < len(p) {
			w.deliveryFailed = true
		}
	}

	w.bytes += int64(len(p))
	if w.remaining > 0 {
		take := int64(len(p))
		if take > w.remaining {
			take = w.remaining
			w.truncated = true
		}
		_, _ = w.buf.Write(p[:take])
		w.remaining -= take
	} else {
		w.truncated = true
	}
	return len(p), nil
}

func (w *subtitleWindowCapture) Flush() {
	if w.deliveryFailed {
		return
	}
	if flusher, ok := w.dst.(http.Flusher); ok {
		flusher.Flush()
	}
}

// fillWaitBound returns this cache's fill-wait bound, falling back to the
// production value for a cache built without one.
func (c *SubtitleCache) fillWaitBound() time.Duration {
	if c != nil && c.fillWaitTimeout > 0 {
		return c.fillWaitTimeout
	}
	return subtitleFillWaitTimeout
}

// waitForSubtitleFill blocks until the in-flight fill identified by wait
// settles (its done channel closes on Commit or Discard), the caller's request
// context is done, or timeout expires. A canceled client returns promptly
// instead of holding a goroutine until the fill finishes. A timeout returns
// errSubtitleFillWaitTimeout so the caller can answer retryable rather than
// starting a second extraction for the same identity.
func waitForSubtitleFill(ctx context.Context, wait <-chan struct{}, timeout time.Duration) error {
	if timeout <= 0 {
		select {
		case <-wait:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-wait:
		return nil
	case <-timer.C:
		return errSubtitleFillWaitTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

// errSubtitleFillWaitTimeout marks a duplicate request that waited out its
// fill-wait bound. It never reaches a client as an error message: the serve
// paths translate it into SubtitleExtractionRetryError.
var errSubtitleFillWaitTimeout = errors.New("subtitle fill wait timed out")

func (c *SubtitleCache) serveCached(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, format string) bool {
	cached, modTime, ok := c.lookup(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return false
	}
	recordSubtitleServeSource(subtitleServeArtifact)
	defer func() { _ = cached.Close() }()
	size := int64(SubtitleTraceBytesUnknown)
	if info, statErr := cached.Stat(); statErr == nil {
		size = info.Size()
	}
	slog.Log(r.Context(), slog.LevelDebug, "subtitle served from cache",
		subtitleTraceAttrs(r.Context(), opts.SourceCodec, SubtitleTracePhaseServe, size, SubtitleTraceOutcomeCacheHit, -1,
			"track", opts.TrackIndex, "format", format)...)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", modTime, cached)
	return true
}

// windowResultKey identifies one exact bounded window: the source identity
// (path mtime/size, or a virtual identity plus its generation bucket), track
// ordinal, output format, and the effective source-time range. A retry that
// changes the position is a different window and is never served stale bytes
// under the wrong coverage.
func (c *SubtitleCache) windowResultKey(opts StreamExtractOpts, format string) (string, bool) {
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed {
		return "", false
	}
	base, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s|%.3f|%.3f|%t", base, start, end, openEnded), true
}

// serveWindowResult serves the last-good copy for the exact window described by
// opts, if one is cached. It never bypasses a committed full-track artifact:
// callers check that first. A hit refreshes the entry's retention.
//
// served reports whether the cache had a copy for this window; err reports a
// failure writing it to the response. A write error is not a cache miss: the
// status line and possibly a prefix of the slice are already on the wire, so
// the caller must terminate the request instead of serving again or starting an
// extraction against the same response.
func (c *SubtitleCache) serveWindowResult(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, format, key string) (served bool, err error) {
	data, ok := c.lookupWindowResult(key)
	if !ok {
		// Exact repeats are the fast path; a client that shifts its playhead by
		// a few seconds asks for a different exact window even though a cached
		// slice already covers the range. Fall back to the tightest covering
		// window only when no exact entry exists. The key is authoritative for
		// the source identity: opts.InputPath may already name a committed
		// artifact rather than the source the window was cached under.
		data, ok = c.lookupCoveringWindowResult(windowResultKeyBase(key), opts)
	}
	if !ok {
		return false, nil
	}
	recordSubtitleServeSource(subtitleServeMemoryWindow)
	slog.Log(r.Context(), slog.LevelDebug, "windowed subtitle served from last-good cache",
		subtitleTraceAttrs(r.Context(), opts.SourceCodec, SubtitleTracePhaseServe, int64(len(data)), SubtitleTraceOutcomeCacheHit, -1,
			"track", opts.TrackIndex, "format", format)...)
	if format == subtitleFormatSUP {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.WriteHeader(http.StatusOK)
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		slog.Log(r.Context(), slog.LevelWarn, "cached subtitle response write failed",
			subtitleTraceAttrs(r.Context(), opts.SourceCodec, SubtitleTracePhaseServe, int64(n), SubtitleTraceOutcomeFailure, -1,
				"track", opts.TrackIndex, "format", format, "error", err)...)
	}
	return true, err
}

func (c *SubtitleCache) lookupWindowResult(key string) ([]byte, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	now := time.Now()
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	entry, ok := c.windowResults[key]
	if !ok {
		return nil, false
	}
	if now.Sub(entry.storedAt) >= subtitleWindowResultTTL {
		delete(c.windowResults, key)
		c.windowBytes -= int64(len(entry.data))
		return nil, false
	}
	// A prefix-derived slice stops being trustworthy the moment the warm's
	// ordering guarantee fails. The disable hook evicts these eagerly; this
	// covers an entry stored either side of the hook.
	if entry.warmPrefix != nil && entry.warmPrefix.prefixDisabled() {
		delete(c.windowResults, key)
		c.windowBytes -= int64(len(entry.data))
		return nil, false
	}
	// An actively-used last-good copy stays available through a long outage.
	entry.storedAt = now
	c.windowResults[key] = entry
	return entry.data, true
}

// lookupCoveringWindowResult serves a cached slice whose source-time range
// contains the requested window. The client's window starts move with the
// playhead, so an exact-key lookup re-demuxes a window that is 95% already
// cached. A covering slice is a correct answer: it is the same source
// identity, track, and output format, and it contains every cue in the
// requested range. The tightest match wins so an oversized slice is not served
// when a closer one is present. Expired/prefix-disabled entries are skipped
// exactly like the exact lookup.
func (c *SubtitleCache) lookupCoveringWindowResult(base string, opts StreamExtractOpts) ([]byte, bool) {
	if c == nil || base == "" {
		return nil, false
	}
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed {
		return nil, false
	}
	now := time.Now()
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	bestKey := ""
	bestSpan := math.Inf(1)
	for key, entry := range c.windowResults {
		if entry.base != base {
			continue
		}
		if now.Sub(entry.storedAt) >= subtitleWindowResultTTL {
			continue
		}
		if entry.warmPrefix != nil && entry.warmPrefix.prefixDisabled() {
			continue
		}
		if !entry.covers(start, end, openEnded) {
			continue
		}
		if span := entry.spanSeconds(); span < bestSpan {
			bestKey, bestSpan = key, span
		}
	}
	if bestKey == "" {
		return nil, false
	}
	entry := c.windowResults[bestKey]
	entry.storedAt = now
	c.windowResults[bestKey] = entry
	return entry.data, true
}

// windowResultKeyBase strips the start/end/open-ended suffix from a window key,
// leaving the source identity the covering lookup compares. The key format is
// `base|%.3f|%.3f|%t`, so three separators are removed from the right; a base
// containing `|` (a path or identity can) stays intact.
func windowResultKeyBase(key string) string {
	last := strings.LastIndexByte(key, '|')
	if last < 0 {
		return ""
	}
	prev := strings.LastIndexByte(key[:last], '|')
	if prev < 0 {
		return ""
	}
	base := key[:prev]
	prev = strings.LastIndexByte(base, '|')
	if prev < 0 {
		return ""
	}
	return base[:prev]
}

// dropWarmTextWindowResults evicts the windows one warm prefix produced when
// that layer disables. Those slices were rendered from a prefix whose
// completeness is no longer proven, so a repeat of the same exact window must
// reach the extraction path rather than replay cues the prefix may have missed.
// Windows served from a committed artifact or a finished extract carry no
// prefix and stay.
func (c *SubtitleCache) dropWarmTextWindowResults(progress *subtitleWarmTextProgress) {
	if c == nil || progress == nil {
		return
	}
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	for key, entry := range c.windowResults {
		if entry.warmPrefix != progress {
			continue
		}
		delete(c.windowResults, key)
		c.windowBytes -= int64(len(entry.data))
	}
}

func (c *SubtitleCache) storeWindowResult(key string, opts StreamExtractOpts, data []byte) {
	c.storeWindowResultFrom(key, opts, data, nil)
}

// storeCompletedWindow keeps a finished window extraction as the last-good copy
// when, and only when, its bytes can hold a cue. A clean ffmpeg exit is not
// enough evidence: a window whose read was cut short can still exit 0 having
// written nothing but the format's header, and storing that makes every later
// request inside the range answer with a document that can never carry a cue —
// a "successful" empty subtitle for the rest of the TTL. Declining to store
// costs a re-extract, which is the safe answer for a range whose track really
// is empty. See subtitleWindowCacheable.
func (c *SubtitleCache) storeCompletedWindow(ctx context.Context, key string, opts StreamExtractOpts, format string, capture *subtitleWindowCapture) {
	if capture == nil || capture.bytes == 0 || capture.truncated || key == "" {
		return
	}
	data := capture.buf.Bytes()
	if !subtitleWindowCacheable(format, data) {
		slog.DebugContext(ctx, "subtitle window holds no cue; not kept as last-good",
			logKeyComponent, logValuePlayback,
			"track", opts.TrackIndex, "format", format, "bytes", len(data))
		return
	}
	c.storeWindowResult(key, opts, data)
}

// storeWindowResultFrom stores data as the last-good copy for key. A non-nil
// warmPrefix marks bytes rendered by an unfinished warm prefix, so the entry is
// dropped if that layer disables. A store that races the disable either sees the
// flag here or lands before the eviction hook, which waits for windowMu.
func (c *SubtitleCache) storeWindowResultFrom(key string, opts StreamExtractOpts, data []byte, warmPrefix *subtitleWarmTextProgress) {
	if c == nil || key == "" || len(data) == 0 || len(data) > subtitleWindowResultMaxEntryBytes {
		return
	}
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed {
		return
	}
	now := time.Now()
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	if warmPrefix != nil && warmPrefix.prefixDisabled() {
		return
	}
	if c.windowResults == nil {
		c.windowResults = make(map[string]subtitleWindowResult)
	}
	if old, exists := c.windowResults[key]; exists {
		c.windowBytes -= int64(len(old.data))
	}
	copyOfData := make([]byte, len(data))
	copy(copyOfData, data)
	c.windowResults[key] = subtitleWindowResult{
		data:         copyOfData,
		storedAt:     now,
		base:         windowResultKeyBase(key),
		startSeconds: start,
		endSeconds:   end,
		openEnded:    openEnded,
		warmPrefix:   warmPrefix,
	}
	c.windowBytes += int64(len(copyOfData))
	c.evictWindowResultsLocked(now)
}

// evictWindowResultsLocked drops expired entries and then the oldest entries
// until the cache is back inside both its byte and count budgets. Caller holds
// windowMu.
func (c *SubtitleCache) evictWindowResultsLocked(now time.Time) {
	for key, entry := range c.windowResults {
		if now.Sub(entry.storedAt) >= subtitleWindowResultTTL {
			delete(c.windowResults, key)
			c.windowBytes -= int64(len(entry.data))
		}
	}
	for len(c.windowResults) > subtitleWindowResultMaxEntries || c.windowBytes > subtitleWindowResultMaxBytes {
		oldestKey := ""
		var oldestAt time.Time
		for key, entry := range c.windowResults {
			if oldestKey == "" || entry.storedAt.Before(oldestAt) {
				oldestKey, oldestAt = key, entry.storedAt
			}
		}
		if oldestKey == "" {
			break
		}
		c.windowBytes -= int64(len(c.windowResults[oldestKey].data))
		delete(c.windowResults, oldestKey)
	}
}

// beginWindowFlight reserves the in-flight marker for one window, or returns
// the marker of a request already extracting it. A request whose range is
// covered by a wider in-flight window joins that flight: after a budget breach
// the detached recovery warm extracts the breached range plus a lead, and the
// retry the 503 asks for is narrower than that warm. Joining it keeps the retry
// from paying the same relay read a second time while the warm is still
// running.
//
// The returned reason is the answer that started the joined flight, when the
// leader published one (StreamExtractOpts.WaitingRefusalReason). A waiter that
// times out answers with it, so the retry after a breach keeps naming the
// window/page state instead of degrading to a generic source failure.
func (c *SubtitleCache) beginWindowFlight(key string, opts StreamExtractOpts) (leader bool, wait <-chan struct{}, refusalReason string) {
	if c == nil || key == "" {
		return true, nil, ""
	}
	base := windowResultKeyBase(key)
	_, start, end, openEnded := opts.WindowCoverage()
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	if existing, ok := c.windowFlights[key]; ok {
		return false, existing.done, existing.refusalReason
	}
	for _, flight := range c.windowFlights {
		if flight.base != base {
			continue
		}
		covering := subtitleWindowResult{
			startSeconds: flight.start,
			endSeconds:   flight.end,
			openEnded:    flight.openEnded,
		}
		if covering.covers(start, end, openEnded) {
			return false, flight.done, flight.refusalReason
		}
	}
	if c.windowFlights == nil {
		c.windowFlights = make(map[string]*subtitleWindowFlight)
	}
	flight := &subtitleWindowFlight{
		done:          make(chan struct{}),
		base:          base,
		start:         start,
		end:           end,
		openEnded:     openEnded,
		refusalReason: opts.WaitingRefusalReason,
	}
	c.windowFlights[key] = flight
	return true, nil, ""
}

func (c *SubtitleCache) endWindowFlight(key string) {
	if c == nil || key == "" {
		return
	}
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	if flight, ok := c.windowFlights[key]; ok {
		delete(c.windowFlights, key)
		close(flight.done)
	}
}

// beginPageFlight registers a windowed ("page") extract that is about to read
// the shared relay source for opts' track. A full-track warm already extracting
// that track is canceled (with ErrSubtitleWarmCanceled) so the page gets the
// pipe; the warm waits for the page to settle and resumes on its own. The
// returned release must be called exactly once.
//
// The map is keyed by the track cache key, not by the exact window: every page
// on the track contends with the same warm, and refcounting keeps a second page
// from waking the warm while the first is still reading. The cancellation and
// the lookup happen under windowMu, so a warm that registers concurrently
// either sees this page in WarmTrackInBackground's settle wait or is canceled
// here — never both missed.
func (c *SubtitleCache) beginPageFlight(opts StreamExtractOpts, format string) func() {
	if c == nil {
		return func() {}
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return func() {}
	}
	c.windowMu.Lock()
	if c.pageFlights == nil {
		c.pageFlights = make(map[string]*subtitlePageFlight)
	}
	flight := c.pageFlights[key]
	if flight == nil {
		flight = &subtitlePageFlight{done: make(chan struct{})}
		c.pageFlights[key] = flight
	}
	flight.refs++
	if run := c.warmRuns[key]; run != nil {
		run.cancel(ErrSubtitleWarmCanceled)
	}
	c.windowMu.Unlock()
	return func() { c.endPageFlight(key) }
}

// endPageFlight releases one page registration. The last release closes the
// flight's done channel, which is what wakes a yielded warm.
func (c *SubtitleCache) endPageFlight(key string) {
	if c == nil || key == "" {
		return
	}
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	flight := c.pageFlights[key]
	if flight == nil {
		return
	}
	flight.refs--
	if flight.refs > 0 {
		return
	}
	delete(c.pageFlights, key)
	close(flight.done)
}

// awaitPageSettled blocks until no page is admitted for the track key, or
// maxWait elapses. It re-checks after each flight settles so a page that starts
// while the warm is waking is still honored. It reports false when the wait
// budget ran out, meaning the warm must yield its slot.
func (c *SubtitleCache) awaitPageSettled(key string, maxWait time.Duration) bool {
	if c == nil || key == "" {
		return true
	}
	deadline := time.Now().Add(maxWait)
	for {
		c.windowMu.Lock()
		flight := c.pageFlights[key]
		c.windowMu.Unlock()
		if flight == nil {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.NewTimer(remaining)
		select {
		case <-flight.done:
			timer.Stop()
		case <-timer.C:
			return false
		}
	}
}

// registerWarmRun publishes a warm's cancel handle for key while it reads. It
// returns nil when a page is already admitted, so the caller waits instead of
// starting a read that would contend immediately. The page check and the
// publication share windowMu with beginPageFlight.
func (c *SubtitleCache) registerWarmRun(key string, cancel context.CancelCauseFunc) *subtitleWarmRun {
	if c == nil {
		return nil
	}
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	if c.pageFlights[key] != nil {
		return nil
	}
	if c.warmRuns == nil {
		c.warmRuns = make(map[string]*subtitleWarmRun)
	}
	run := &subtitleWarmRun{cancel: cancel}
	c.warmRuns[key] = run
	return run
}

// unregisterWarmRun removes run from the registry. It compares the pointer so
// a newer warm for the same key (a restart) is never removed by an older one.
func (c *SubtitleCache) unregisterWarmRun(key string, run *subtitleWarmRun) {
	if c == nil || run == nil {
		return
	}
	c.windowMu.Lock()
	defer c.windowMu.Unlock()
	if c.warmRuns[key] == run {
		delete(c.warmRuns, key)
	}
}

// windowWaitBound returns this cache's duplicate-window wait bound, falling back
// to the production value for a cache built without one.
func (c *SubtitleCache) windowWaitBound() time.Duration {
	if c != nil && c.windowWaitTimeout > 0 {
		return c.windowWaitTimeout
	}
	return subtitleWindowWaitTimeout
}

// SetWindowWaitTimeoutForTest overrides the duplicate-window wait bound so a
// test can exercise the retryable answer without waiting out the production
// bound. A zero or negative value restores the production one.
func (c *SubtitleCache) SetWindowWaitTimeoutForTest(bound time.Duration) {
	if c == nil {
		return
	}
	c.windowWaitTimeout = bound
}

// waitForWindowFlight waits for the leading extraction to settle, bounded by
// subtitleWindowWaitTimeout. A waiter that times out answers retryable rather
// than spawning a duplicate. Returning an error means the caller must stop;
// returning nil means the caller may re-check the cache and backoff. A flight
// the recovery warm started carries its refusal reason, so a timeout keeps the
// window/page code and Retry-After the first refusal published instead of
// answering a generic source failure.
func (c *SubtitleCache) waitForWindowFlight(ctx context.Context, wait <-chan struct{}, refusalReason string) error {
	timer := time.NewTimer(c.windowWaitBound())
	defer timer.Stop()
	select {
	case <-wait:
		return nil
	case <-timer.C:
		return &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter, Reason: refusalReason}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// extractionCoolingDownFor is extractionCoolingDown with the fresh-source
// retry exemption: a caller that replaced InputPath with a newly resolved URL
// (FreshSourceRetry) is deliberately re-opening the provider for the same
// identity, so the failed attempt's backoff must not suppress it.
func (c *SubtitleCache) extractionCoolingDownFor(opts StreamExtractOpts, format string) (time.Duration, bool) {
	if opts.FreshSourceRetry {
		return 0, false
	}
	remaining, _, cooling := c.extractionCoolingDown(opts, format)
	return remaining, cooling
}

// extractionCoolingDownReasonFor is extractionCoolingDownFor plus the reason the
// arming failure answered with, so a window retry inside the backoff keeps the
// cost refusal instead of a generic source failure.
func (c *SubtitleCache) extractionCoolingDownReasonFor(opts StreamExtractOpts, format string) (time.Duration, string, bool) {
	if opts.FreshSourceRetry {
		return 0, "", false
	}
	return c.extractionCoolingDown(opts, format)
}

// extractionCoolingDown reports the remaining per-identity backoff after a
// retryable upstream extraction failure. No entry means the next attempt is
// allowed. A lapsed cooldown keeps the failure streak so the next failure
// escalates; only a success or the history horizon clears it.
func (c *SubtitleCache) extractionCoolingDown(opts StreamExtractOpts, format string) (time.Duration, string, bool) {
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return 0, "", false
	}
	now := time.Now()
	c.failureMu.Lock()
	defer c.failureMu.Unlock()
	guard, ok := c.failureGuard[key]
	if !ok {
		return 0, "", false
	}
	if now.Before(guard.retryAt) {
		return guard.retryAt.Sub(now), guard.reason, true
	}
	if now.Sub(guard.lastFailure) >= subtitleExtractionFailureHistoryTTL {
		delete(c.failureGuard, key)
	}
	return 0, "", false
}

// noteExtractionFailure extends the per-identity backoff after a retryable
// upstream failure. The first failure pauses for
// subtitleExtractionFailureBaseCooldown; each consecutive failure doubles it up
// to subtitleExtractionFailureMaxCooldown, even when the previous cooldown had
// already lapsed. Only a success or the history horizon restarts the streak.
func (c *SubtitleCache) noteExtractionFailure(opts StreamExtractOpts, format string) {
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return
	}
	now := time.Now()
	c.failureMu.Lock()
	defer c.failureMu.Unlock()
	if c.failureGuard == nil {
		c.failureGuard = make(map[string]subtitleExtractionGuard)
	}
	if _, exists := c.failureGuard[key]; !exists && len(c.failureGuard) >= subtitleExtractionFailureMaxEntries {
		c.sweepFailureGuardLocked(now)
	}
	guard := c.failureGuard[key]
	if guard.lastFailure.IsZero() || now.Sub(guard.lastFailure) >= subtitleExtractionFailureHistoryTTL {
		guard.failures = 0
	}
	guard.failures++
	backoff := subtitleExtractionFailureBaseCooldown
	for i := 1; i < guard.failures && backoff < subtitleExtractionFailureMaxCooldown; i++ {
		backoff *= 2
	}
	if backoff > subtitleExtractionFailureMaxCooldown {
		backoff = subtitleExtractionFailureMaxCooldown
	}
	guard.retryAt = now.Add(backoff)
	guard.lastFailure = now
	// A new failure re-arms the answer: the reason the previous one carried
	// belongs to that failure and must not survive an upstream error that follows
	// it. A cost refusal re-records its own reason right after this call.
	guard.reason = ""
	c.failureGuard[key] = guard
}

// noteExtractionRefusalReason records the problem code the failure that just
// armed this identity's backoff answered with, so a retry inside the backoff
// keeps it. It must be called after noteExtractionFailure for the same
// key/opts/format.
func (c *SubtitleCache) noteExtractionRefusalReason(opts StreamExtractOpts, format, reason string) {
	if reason == "" {
		return
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return
	}
	c.failureMu.Lock()
	defer c.failureMu.Unlock()
	guard, ok := c.failureGuard[key]
	if !ok {
		return
	}
	guard.reason = reason
	c.failureGuard[key] = guard
}

// noteExtractionSuccess clears an identity's backoff once extraction (or its
// cache commit) succeeds.
func (c *SubtitleCache) noteExtractionSuccess(opts StreamExtractOpts, format string) {
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return
	}
	c.failureMu.Lock()
	delete(c.failureGuard, key)
	c.failureMu.Unlock()
}

// sweepFailureGuardLocked drops streaks whose quiet horizon has passed, then,
// while the map is at its cap, the least recently failed entry. Lapsed
// cooldowns stay in the map because they carry the failure count that lets a
// sustained outage escalate; subtitleExtractionFailureHistoryTTL is the
// separate bound on that retained history. Caller holds failureMu.
func (c *SubtitleCache) sweepFailureGuardLocked(now time.Time) {
	for key, guard := range c.failureGuard {
		if now.Sub(guard.lastFailure) >= subtitleExtractionFailureHistoryTTL {
			delete(c.failureGuard, key)
		}
	}
	if len(c.failureGuard) < subtitleExtractionFailureMaxEntries {
		return
	}
	victim := ""
	var victimAt time.Time
	for key, guard := range c.failureGuard {
		if victim == "" || guard.lastFailure.Before(victimAt) || (guard.lastFailure.Equal(victimAt) && key < victim) {
			victim, victimAt = key, guard.lastFailure
		}
	}
	if victim != "" {
		delete(c.failureGuard, victim)
	}
}

// subtitleCacheFormat maps a codec/target pair to the cache format token,
// normalizing ffmpeg's "webvtt" muxer name to the cache's "vtt" token. It is
// the one mapping ServeExtract, WarmTrackInBackground, and committed-entry
// queries share, so a lookup can never disagree with a fill.
func subtitleCacheFormat(codec, targetFormat string) string {
	_, format := streamExtractOutput(codec, targetFormat)
	if format == subtitleMuxerWebVTT {
		return SubtitleFormatVTTV3
	}
	return format
}

// CommittedTextArtifact is a resolved reference to a committed full-track
// text (VTT/ASS) cache entry. ResolveCommittedTextEntry binds the exact cache
// path under the current invalidation coordinates; a caller can hand the token
// back through StreamExtractOpts.PinnedTextArtifact so the serve uses that
// same artifact rather than re-resolving it. Cache presence alone is not proof
// of track identity: the token is what makes the identity check and the read
// consistent, so only a token — never HasCommittedTextEntry's bool — may be
// used to skip validation.
type CommittedTextArtifact struct {
	path   string
	format string
}

// ResolveCommittedTextEntry resolves the committed full-track text artifact
// for the given source identity + track ordinal and returns a token that pins
// its exact path. codec and targetFormat are the same values passed to
// ServeExtract, and the format is derived through the same mapping, so the
// token is exactly what a windowed ServeExtract would read. ok is false for a
// bitmap (PGS) codec, an unkeyable source, a missing entry, or a nil cache.
//
// Unlike HasCommittedTextEntry, the returned token is bound to the resolved
// path: passing it back via StreamExtractOpts.PinnedTextArtifact guarantees
// ServeExtractWithResult serves that artifact (or reports
// ErrCommittedTextArtifactGone), so a generation-bucket rollover or an
// eviction between the identity check and the read cannot silently invalidate
// the decision. Callers that skip track-identity validation because an
// artifact exists must use this method and the pinned token.
func (c *SubtitleCache) ResolveCommittedTextEntry(inputPath, cacheIdentity string, trackIndex int, codec, targetFormat string) (CommittedTextArtifact, bool) {
	if c == nil {
		return CommittedTextArtifact{}, false
	}
	format := subtitleCacheFormat(codec, targetFormat)
	if format != SubtitleFormatVTTV3 && format != subtitleFormatASS {
		return CommittedTextArtifact{}, false
	}
	path, _, ok := c.cachedFormatEntryPath(inputPath, cacheIdentity, trackIndex, format)
	if !ok {
		return CommittedTextArtifact{}, false
	}
	return CommittedTextArtifact{path: path, format: format}, true
}

// resolveCommittedTextArtifact returns the artifact ServeExtract should use:
// the caller's pinned token when present (authoritative), otherwise a fresh
// resolution for an unpinned request.
func (c *SubtitleCache) resolveCommittedTextArtifact(opts StreamExtractOpts) (CommittedTextArtifact, bool) {
	if opts.PinnedTextArtifact != nil {
		return *opts.PinnedTextArtifact, true
	}
	return c.ResolveCommittedTextEntry(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, opts.SourceCodec, opts.TargetFormat)
}

// HasCommittedTextEntry reports whether a committed full-track text (VTT/ASS)
// artifact exists for the given source identity + track ordinal. codec and
// targetFormat are the same values passed to ServeExtract, and the format is
// derived through the same mapping.
//
// This is a non-binding snapshot: because the check and the read are separate
// operations, a hit does not prove the track identity a later ServeExtract
// will read (a generation-bucket rollover or eviction can turn that read into
// a miss). Callers that intend to skip track-identity validation must resolve
// the artifact with ResolveCommittedTextEntry and pin it via
// StreamExtractOpts.PinnedTextArtifact instead. A bitmap (PGS) codec, an
// unkeyable source, or a nil cache reads as false.
func (c *SubtitleCache) HasCommittedTextEntry(inputPath, cacheIdentity string, trackIndex int, codec, targetFormat string) bool {
	if c == nil {
		return false
	}
	_, ok := c.ResolveCommittedTextEntry(inputPath, cacheIdentity, trackIndex, codec, targetFormat)
	return ok
}

// HasCommittedEntry reports whether a committed full-track artifact exists for
// the source identity + track ordinal in the format this codec/target maps to:
// VTT, ASS, or SUP. It generalizes HasCommittedTextEntry to every sidecar class
// so the implicit-window gate can serve an already-committed track whole
// (cheap) instead of slicing it, for text, ASS, and PGS alike.
//
// Like HasCommittedTextEntry this is a non-binding snapshot: it exists for
// admission decisions, not to prove the identity a later read will use. Callers
// that skip track-identity validation must still resolve and pin the artifact
// with ResolveCommittedTextEntry (text/ASS); PGS has no pinned-token path and
// keeps its mandatory drift probe. A codec with no sidecar class, an unkeyable
// source, or a nil cache reads as false.
func (c *SubtitleCache) HasCommittedEntry(inputPath, cacheIdentity string, trackIndex int, codec, targetFormat string) bool {
	if c == nil {
		return false
	}
	format := subtitleCacheFormat(codec, targetFormat)
	if format == "" {
		return false
	}
	_, _, ok := c.cachedFormatEntryPath(inputPath, cacheIdentity, trackIndex, format)
	return ok
}

// HasWarmingEntry reports whether a full-track fill is already in flight for
// the source identity + track ordinal in the format this codec/target maps to:
// the detached warm a windowed miss starts, a request-owned fill, or another
// waiting request's fill. Like HasCommittedEntry this is a non-binding snapshot
// for admission decisions; it never reserves or mutates the fill.
//
// It distinguishes a client's upgrade probe from a cold whole-track fetch: a
// probe targets a track whose full-track fill is already warming (the probe
// exists to discover that commit), whereas a cold fetch has no fill to join.
// A codec with no sidecar class, an unkeyable source, or a nil cache reads as
// false.
func (c *SubtitleCache) HasWarmingEntry(inputPath, cacheIdentity string, trackIndex int, codec, targetFormat string) bool {
	if c == nil || c.dir() == "" {
		return false
	}
	format := subtitleCacheFormat(codec, targetFormat)
	if format == "" {
		return false
	}
	identity, modTime, size, ok := subtitleCacheSource(inputPath, cacheIdentity, time.Now())
	if !ok {
		return false
	}
	key := subtitleCacheFormatKey(identity, trackIndex, modTime, size, format)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, warming := c.inflight[key]
	return warming
}

// warnDemuxBusy reports that this serve shed a client-facing subtitle read
// because the relay source was saturated. The first shed per interval warns;
// repeats in the same interval drop to debug, so a client retry loop keeps a
// diagnostic trail without flooding the log.
func (c *SubtitleCache) warnDemuxBusy(ctx context.Context, opts StreamExtractOpts, format string) {
	interval := subtitleDemuxBusyWarnEvery
	now := time.Now
	if c != nil {
		if c.demuxBusyWarnEvery > 0 {
			interval = c.demuxBusyWarnEvery
		}
		if c.demuxBusyWarnNow != nil {
			now = c.demuxBusyWarnNow
		}
	}
	shouldWarn := true
	if c != nil {
		at := now()
		c.demuxBusyWarnMu.Lock()
		if !c.demuxBusyWarnAt.IsZero() && at.Sub(c.demuxBusyWarnAt) < interval {
			shouldWarn = false
		} else {
			c.demuxBusyWarnAt = at
		}
		c.demuxBusyWarnMu.Unlock()
	}
	attrs := []any{
		logKeyComponent, logValuePlayback,
		"input", opts.InputPath,
		"track", opts.TrackIndex,
		"format", format,
		"retry_after_seconds", int(subtitleFillBusyRetryAfter.Seconds()),
	}
	if !shouldWarn {
		slog.DebugContext(ctx, "subtitle extract deferred: relay source saturated", attrs...)
		return
	}
	slog.WarnContext(ctx, "subtitle extract shed: relay source saturated", attrs...)
}

// ServeSUPExtract serves the .sup extract for one source+track described by
// opts (opts.Writer is ignored; the cache supplies it). Full-track requests
// (no AllowWindow): a cache hit is served with http.ServeContent (Range
// support, Content-Length, Last-Modified from the source file's mtime,
// revalidatable instead of no-store); a miss invokes extract with a writer
// that streams to the client while teeing bytes into a temp file, atomically
// published as the cache entry on clean extract exit and discarded on any
// error (ffmpeg failure or client disconnect) — a partial entry is never
// served. A request that arrives while a fill is already in flight waits for
// that fill and serves its committed entry rather than running a second full
// demux; only when no fill can be owned does it fall back to an uncached
// stream. Windowed requests (opts.AllowWindow): the output covers only a
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
	start := time.Now()

	if c.serveSUPEntry(w, r, opts) {
		return nil
	}

	if retryAfter, cooling := c.extractionCoolingDownFor(opts, subtitleFormatSUP); cooling {
		return &SubtitleExtractionRetryError{RetryAfter: retryAfter}
	}

	// Reserve before committing response headers. If another request owns the
	// fill, wait here so a committed result can use ServeContent (and retain
	// range/Last-Modified semantics) rather than becoming a streamed 200.
	if c != nil { //nolint:nestif // ServeSUPExtract exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		reserved, inflight := c.reserveFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP, true)
		if reserved != nil {
			reserved.Discard()
		}
		if inflight != nil {
			if err := waitForSubtitleFill(r.Context(), inflight.done, c.fillWaitBound()); err != nil {
				return err
			}
			if c.serveSUPEntry(w, r, opts) {
				return nil
			}
		}
	}

	// A whole-track PGS serve is a heavy relay read: the request path's for a
	// client whole-track fetch, detached for the param-less probe. Admit it in
	// its class per relay source before the 200 commits, so a taken budget
	// answers retryable instead of stacking another full demux on the same pipe.
	admittedCtx, releaseRelayDemux, admitErr := acquireRelayDemuxRead(relayDemuxAdmissionContext(r.Context(), opts), opts.InputPath)
	if admitErr != nil {
		c.warnDemuxBusy(r.Context(), opts, subtitleFormatSUP)
		return &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter}
	}
	defer releaseRelayDemux()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")

	counter := &subtitleCountingWriter{w: w}
	warmOverlap := c.warmInFlight(opts, subtitleFormatSUP)
	overlapStart := time.Now()
	recordSubtitleServeSource(subtitleServePGSSupSource)
	err := c.streamFullTrack(admittedCtx, counter, opts, subtitleFormatSUP, extract)
	if warmOverlap {
		recordSubtitleWarmPageOverlap(subtitleFormatSUP, time.Since(overlapStart))
	}
	outcome := subtitleTraceOutcome(r.Context(), err)
	level := slog.LevelInfo
	if outcome != SubtitleTraceOutcomeSuccess {
		level = slog.LevelWarn
	}
	slog.Log(r.Context(), level, "subtitle extract served",
		subtitleTraceAttrs(r.Context(), opts.SourceCodec, subtitlePhaseForOutput(opts.SourceCodec, opts.TargetFormat),
			counter.bytes, outcome, time.Since(start),
			"track", opts.TrackIndex, "format", subtitleFormatSUP)...)
	return err
}

// serveSUPEntry serves a committed full-track .sup entry, returning false on a
// miss (or when caching is disabled). On a hit it writes the octet-stream
// headers and delegates range/HEAD/content-length handling to
// http.ServeContent; lookup bumps the entry's mtime for LRU recency.
func (c *SubtitleCache) serveSUPEntry(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts) bool {
	cached, modTime, ok := c.lookup(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		return false
	}
	recordSubtitleServeSource(subtitleServePGSSupArtifact)
	defer func() { _ = cached.Close() }()
	size := int64(SubtitleTraceBytesUnknown)
	if info, statErr := cached.Stat(); statErr == nil {
		size = info.Size()
	}
	slog.Log(r.Context(), slog.LevelDebug, "subtitle stream served from cache",
		subtitleTraceAttrs(r.Context(), opts.SourceCodec, SubtitleTracePhaseServe, size, SubtitleTraceOutcomeCacheHit, -1,
			"track", opts.TrackIndex, "format", subtitleFormatSUP)...)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", modTime, cached)
	return true
}

// serveWindowedSUP streams a windowed slice of the track. The output is a
// position-dependent slice, but the cache still speeds it up: with a committed
// full-track entry the extract's input is rewritten to the cached .sup (15-80 MB,
// so the -ss scan is near-instant versus re-demuxing a multi-GB source); without
// one, a background warm is started so later windows — the client re-fetches on
// every seek — hit the fast path.
//
// The exact window is also kept in memory as the last-good copy, so a repeat
// after an upstream failure is served without re-running ffmpeg; identical
// concurrent windows are coalesced into one extract; and a retryable upstream
// failure puts the source identity in a short backoff. Retryable upstream
// failures are retried in-request by runExtract before any bytes are written.
func (c *SubtitleCache) serveWindowedSUP(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SUPExtractFunc) error {
	if err := r.Context().Err(); err != nil {
		return err
	}
	start := time.Now()
	// Compute the last-good window key before the committed-entry fast path
	// rewrites InputPath to the cached .sup (and may re-anchor the seek). The
	// key must stay on the original source identity and requested range so a
	// window cached from the source matches the same window extracted from
	// the artifact.
	sourceOpts := opts
	windowKey, _ := c.windowResultKey(opts, subtitleFormatSUP)
	served, serveErr := c.serveWindowResult(w, r, opts, subtitleFormatSUP, windowKey)
	if serveErr != nil {
		// The 200 and part of the slice are already on the wire; serving or
		// extracting again would corrupt the response.
		return serveErr
	}
	if served {
		return nil
	}
	// Every page below this point would read the source. A committed full-track
	// artifact makes the page a local read instead, so it is exempt; without one
	// two rules run before any source read:
	//
	//   - a page the track's progressive artifact already covers is copied out
	//     of it, and a page beyond its watermark answers retryable with that
	//     coverage (see subtitle_progressive_sup.go);
	//   - a page whose own window cannot be read inside one page extract is
	//     refused before the read, because refusing after it is the wall clock
	//     this gate exists to remove (see subtitle_page_cost.go). The window,
	//     not the container prefix: the extract seeks to the page.
	//
	// Both must precede the packet-source path: indexing the container to cut one
	// page is a second read of the same source, and the page's window is what it
	// costs.
	cachedPath, _, hasArtifact := c.cachedEntryPath(opts.InputPath, opts.CacheIdentity, opts.TrackIndex)
	if !hasArtifact {
		if served, progressiveErr := c.serveProgressiveSUPPage(w, r, sourceOpts, extract); served || progressiveErr != nil {
			return progressiveErr
		}
		if refusal := c.pageWindowRefusal(sourceOpts); refusal != nil {
			return refusal
		}
	}
	if hasArtifact && opts.DurationSeconds <= 0 {
		// A bounded window (a page) reads the source first and falls back to the
		// committed artifact below; a whole-track window reads the artifact, the
		// small local copy, instead of demuxing the container again.
		opts.InputPath = cachedPath
		opts.InputIsExtractedSup = true
	}
	// For bounded and uncached open-ended windows, consult the embedded source
	// before any raw demux. This is the safe PGS page path: ReadPackets returns
	// the packets that show anywhere in [from, to), including display-set
	// segments that started just before the window and are still on screen, so
	// a bitmap straddling the boundary is not lost. A direct FFmpeg "-ss" slice
	// of the source (the fallthrough below) cannot make that guarantee: an
	// input seek lands on whatever packet precedes the seek point, not on the
	// opening display set. cachedSupSeekSlack and windowFromCachedSup only
	// compensate on the extracted-artifact path. The raw slice is a last-resort
	// fallback, not a page path: a source without usable cues should be left on
	// the open-ended (whole-track) path, which warms its .sup artifact and is
	// the safe steady state.
	// Cached open-ended windows intentionally fall through to the artifact path
	// above.
	if opts.DurationSeconds > 0 || (opts.DurationSeconds <= 0 && opts.InputPath == sourceOpts.InputPath) {
		if served, sourceErr := c.serveWindowFromSource(r.Context(), w, opts, extract); served {
			if sourceErr == nil && !sourceOpts.DisableBackgroundWarm {
				c.WarmTrackInBackground(sourceOpts, c.sourced(extract), subtitleTraceRequestID(r.Context()))
			}
			return sourceErr
		}
	}

	// A committed full-track entry is the best last-good copy, so it is
	// preferred over the failure backoff: an artifact-backed extract reads a
	// local file and a previous provider failure does not apply to it.
	artifactBacked := hasArtifact
	if artifactBacked {
		slog.DebugContext(r.Context(), "windowed subtitle extract using cached full track",
			"input", opts.InputPath, "track", opts.TrackIndex, "cache_entry", cachedPath)
		opts.InputPath = cachedPath
		opts.InputIsExtractedSup = true
		if firstCue, err := readSupFirstCue(cachedPath); err == nil {
			opts = windowFromCachedSup(opts, firstCue)
		}
	} else if refusal := c.windowedSUPMissRefusal(sourceOpts); refusal != nil {
		return refusal
	} else if !opts.DisableBackgroundWarm {
		c.WarmInBackground(opts, extract, subtitleTraceRequestID(r.Context()))
	}

	// Coalesce identical in-flight windows: a duplicate request waits for the
	// leader and serves the copy it stores instead of spawning a second ffmpeg.
	// A failed leader leaves either a cached window or the failure backoff
	// behind, both re-checked after the wait.
	leader := false
	for attempt := 0; windowKey != "" && attempt < subtitleWindowWaitRetries; attempt++ {
		var wait <-chan struct{}
		var refusalReason string
		leader, wait, refusalReason = c.beginWindowFlight(windowKey, opts)
		if leader {
			break
		}
		if err := c.waitForWindowFlight(r.Context(), wait, refusalReason); err != nil {
			return err
		}
		served, serveErr := c.serveWindowResult(w, r, opts, subtitleFormatSUP, windowKey)
		if serveErr != nil {
			return serveErr
		}
		if served {
			return nil
		}
		if !artifactBacked {
			if retryAfter, cooling := c.extractionCoolingDownFor(sourceOpts, subtitleFormatSUP); cooling {
				return &SubtitleExtractionRetryError{RetryAfter: retryAfter}
			}
		}
	}
	if leader {
		// Every leader path — a re-check hit, success, failure, cancellation,
		// or a write error — must release the flight so waiters proceed.
		defer c.endWindowFlight(windowKey)
		// A previous leader may have stored its slice between our lookup and
		// our successful reservation; serve that instead of extracting again.
		served, serveErr := c.serveWindowResult(w, r, opts, subtitleFormatSUP, windowKey)
		if serveErr != nil {
			return serveErr
		}
		if served {
			return nil
		}
	}

	// A windowed PGS serve is a heavy relay read in its own admission class
	// (request path, unless this request is the param-less probe): admit it per
	// relay source before the 200 commits so a taken budget still answers
	// retryable.
	admittedCtx, releaseRelayDemux, admitErr := acquireRelayDemuxRead(relayDemuxAdmissionContext(r.Context(), opts), opts.InputPath)
	if admitErr != nil {
		c.warnDemuxBusy(r.Context(), opts, subtitleFormatSUP)
		return &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter}
	}
	defer releaseRelayDemux()

	// A page extract that reads the source contends with a full-track warm for
	// the same PGS track. Artifact-backed windows scan the cached .sup instead,
	// so they have nothing to yield.
	if !artifactBacked {
		endPage := c.beginPageFlight(sourceOpts, subtitleFormatSUP)
		defer endPage()
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")

	counter := &subtitleCountingWriter{w: w}
	var writer io.Writer = counter
	var capture *subtitleWindowCapture
	if leader {
		// Keep the successful slice as the last-good copy. The capture bound
		// is per response, so a huge window is streamed but not retained.
		capture = &subtitleWindowCapture{dst: counter, remaining: subtitleWindowResultMaxEntryBytes}
		writer = capture
	}
	opts.Writer = writer
	// Price the next page from the rate this one reads the source at. An
	// artifact-backed page reads a local file, so it says nothing about the
	// provider and is left alone.
	if !artifactBacked && opts.NoteSourceReadRate == nil {
		opts.NoteSourceReadRate = func(bytesPerSecond float64) {
			c.notePageReadRate(sourceOpts.CacheIdentity, bytesPerSecond)
		}
	}
	// A coalesced extraction belongs to the flight, not only to the leader
	// request. Detach it from the leader's cancellation (bounded by the
	// timeout) so a leader disconnect does not throw away work that waiters
	// can still consume from the last-good cache.
	extractCtx := admittedCtx
	if leader {
		var cancel context.CancelFunc
		extractCtx, cancel = context.WithTimeout(
			context.WithoutCancel(admittedCtx),
			subtitleWindowExtractTimeout,
		)
		defer cancel()
	}
	warmOverlap := c.warmInFlight(sourceOpts, subtitleFormatSUP)
	overlapStart := time.Now()
	recordSubtitleServeSource(subtitleServePGSSupSource)
	err := c.runExtract(extractCtx, opts, extract, counter, nil)
	if warmOverlap {
		recordSubtitleWarmPageOverlap(subtitleFormatSUP, time.Since(overlapStart))
	}
	if err != nil {
		if counter.bytes == 0 && errors.Is(err, ErrSubtitlePageTooCostly) {
			// The page's extraction was killed for reading too much of the
			// container prefix. Answer the retryable shape the client already
			// backs off from, and arm the existing extraction backoff so a
			// client retry does not pay the same read again; the track's
			// progressive artifact is what serves the page once it has parsed
			// that far.
			recordSubtitlePageTooCostly()
			c.noteExtractionFailure(sourceOpts, subtitleFormatSUP)
			err = c.pageTooCostlyRetry(sourceOpts)
		} else if counter.bytes == 0 && r.Context().Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			// The page ran out the window extract deadline before emitting a
			// byte. That is not a server failure: the page could not be read
			// from the source in time, so answer the same retryable shape and
			// let the client back off instead of spending its terminal budget
			// on a 500.
			recordSubtitlePageTimeout()
			err = c.pageTooCostlyRetry(sourceOpts)
		} else if r.Context().Err() == nil && IsSubtitleUpstreamError(err) {
			// Recorded against the original source identity (sourceOpts), not
			// the rewritten cached-artifact path.
			c.noteExtractionFailure(sourceOpts, subtitleFormatSUP)
		}
	} else {
		c.noteExtractionSuccess(sourceOpts, subtitleFormatSUP)
		if pageStart, _, _ := progressiveSUPWindow(sourceOpts); true {
			c.clearPageRecovery(sourceOpts, pageStart)
		}
		c.storeCompletedWindow(r.Context(), windowKey, sourceOpts, subtitleFormatSUP, capture)
	}
	outcome := subtitleTraceOutcome(r.Context(), err)
	level := slog.LevelInfo
	if outcome != SubtitleTraceOutcomeSuccess {
		level = slog.LevelWarn
	}
	slog.Log(r.Context(), level, "subtitle extract served",
		subtitleTraceAttrs(r.Context(), opts.SourceCodec, subtitlePhaseForOutput(opts.SourceCodec, opts.TargetFormat),
			counter.bytes, outcome, time.Since(start),
			"track", opts.TrackIndex, "format", subtitleFormatSUP)...)
	return err
}

// windowedSUPMissRefusal returns the retryable answer while this source identity
// is inside its extraction backoff, or nil when an extract may proceed. It runs
// after the committed-artifact check, so a page that reads the cache never pays
// for a previous provider failure, and before any header or provider read, so the
// handler can answer 503 + Retry-After cleanly.
//
// A bounded bitmap page is answered by its track's progressive artifact, not by
// the source extract this cooldown guards, so the refusal keeps naming that
// state and the range the artifact covers. A generic subtitle_source_unavailable
// here dropped both: the handler starts the recovery fill only for a page
// refusal, so the retry could only repeat the read that earned the cooldown.
func (c *SubtitleCache) windowedSUPMissRefusal(opts StreamExtractOpts) error {
	retryAfter, cooling := c.extractionCoolingDownFor(opts, subtitleFormatSUP)
	if !cooling {
		return nil
	}
	if refusal := c.bitmapPageCooldownRefusal(opts, retryAfter); refusal != nil {
		return refusal
	}
	return &SubtitleExtractionRetryError{RetryAfter: retryAfter}
}

// bitmapPageCooldownRefusal names the state a bounded bitmap page is waiting on
// while its identity is inside an extraction cooldown. With no progressive fill
// the page has nothing behind it: the answer is the cost refusal, which is also
// what makes the handler start the fill that will serve it. Once a fill is
// running the page either answered from it already (see serveProgressiveSUPPage)
// or cannot use it yet, so the cooldown's own delay is the honest answer and nil
// is returned.
func (c *SubtitleCache) bitmapPageCooldownRefusal(opts StreamExtractOpts, retryAfter time.Duration) error {
	if !opts.pageReadWatch() {
		return nil
	}
	if windowed, _, _, openEnded := opts.WindowCoverage(); !windowed || openEnded {
		return nil
	}
	if fill, _ := c.progressiveSUPArtifact(opts); fill != nil {
		return nil
	}
	start, _, _ := progressiveSUPWindow(opts)
	return c.pageRecoveryRefusal(opts, start, 0, max(retryAfter, subtitlePageCostlyRetryAfter), SubtitlePageTooCostlyReason)
}

// formatEntryKey computes the in-flight/cache key for a source identity,
// track ordinal, and format without creating a fill. It mirrors
// beginFillOrWait's key derivation exactly, so warm admission and the fill it
// guards always agree. ok is false when the cache is disabled or the source
// cannot be keyed.
func (c *SubtitleCache) formatEntryKey(inputPath, cacheIdentity string, trackIndex int, format string) (string, bool) {
	if c == nil || c.dir() == "" {
		return "", false
	}
	identity, modTime, size, ok := subtitleCacheSource(inputPath, cacheIdentity, time.Now())
	if !ok {
		return "", false
	}
	return subtitleCacheFormatKey(identity, trackIndex, modTime, size, format), true
}

// warmAdmitted reports whether a background warm may start for the cache key,
// rate-limiting identities whose warm recently failed or was abandoned. It
// reads only; noteWarmOutcome records the result.
func (c *SubtitleCache) warmAdmitted(key string, now time.Time) bool {
	if c == nil {
		return false
	}
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	guard, ok := c.warmGuard[key]
	// At the cap, drop entries whose cooldown has lapsed. They no longer
	// suppress anything, so this keeps the map from retaining memory the
	// admission check will never consult.
	if len(c.warmGuard) >= subtitleWarmGuardMaxEntries {
		c.pruneExpiredWarmGuardLocked(now)
	}
	if !ok {
		return true
	}
	return !now.Before(guard.retryAt)
}

// pruneExpiredWarmGuardLocked drops entries whose cooldown has lapsed. Caller
// holds warmMu.
func (c *SubtitleCache) pruneExpiredWarmGuardLocked(now time.Time) {
	for k, guard := range c.warmGuard {
		if !now.Before(guard.retryAt) {
			delete(c.warmGuard, k)
		}
	}
}

// sweepWarmGuardLocked makes room when the map is at its cap. It first drops
// lapsed entries (behaviorally dead), then, if every remaining entry is still
// live, evicts the one closest to lapsing. The soonest-expiring entry would
// stop suppressing within the shortest time anyway. Caller holds warmMu.
func (c *SubtitleCache) sweepWarmGuardLocked(now time.Time) {
	if len(c.warmGuard) < subtitleWarmGuardMaxEntries {
		return
	}
	c.pruneExpiredWarmGuardLocked(now)
	if len(c.warmGuard) < subtitleWarmGuardMaxEntries {
		return
	}
	victim := ""
	var victimAt time.Time
	for k, guard := range c.warmGuard {
		if victim == "" || guard.retryAt.Before(victimAt) || (guard.retryAt.Equal(victimAt) && k < victim) {
			victim = k
			victimAt = guard.retryAt
		}
	}
	if victim != "" {
		delete(c.warmGuard, victim)
	}
}

// noteWarmOutcome records a warm result for the cache key. Success clears the
// identity's backoff; failure (or abandonment) increments it and sets an
// exponentially growing retry time capped at
// subtitleWarmFailureMaxCooldown, so repeated windowed misses on a source
// that cannot be warmed do not each spawn a full-track remote read.
func (c *SubtitleCache) noteWarmOutcome(key string, success bool, now time.Time) {
	if c == nil || key == "" {
		return
	}
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	if success {
		delete(c.warmGuard, key)
		return
	}
	if c.warmGuard == nil {
		c.warmGuard = make(map[string]subtitleWarmGuard)
	}
	// Make room before inserting a new entry. Refreshing an existing entry does
	// not grow the map, so it never needs the sweep and never evicts the entry
	// it is about to refresh.
	if _, exists := c.warmGuard[key]; !exists && len(c.warmGuard) >= subtitleWarmGuardMaxEntries {
		c.sweepWarmGuardLocked(now)
	}
	guard := c.warmGuard[key]
	guard.failures++
	backoff := subtitleWarmFailureBaseCooldown
	for i := 1; i < guard.failures && backoff < subtitleWarmFailureMaxCooldown; i++ {
		backoff *= 2
	}
	if backoff > subtitleWarmFailureMaxCooldown {
		backoff = subtitleWarmFailureMaxCooldown
	}
	guard.retryAt = now.Add(backoff)
	c.warmGuard[key] = guard
}

// WarmInBackground starts a detached full-track extract that fills the cache
// entry for opts' source+track, so future windowed requests can extract from
// the small cached .sup instead of the original file. The warm runs on a
// background context with a generous timeout — it must survive the request
// that triggered it. requestID is the triggering request's ID, captured at the
// boundary because the detached context no longer carries it; it is threaded
// into the warm's traces so they join that request. BeginFill's in-flight
// coalescing guarantees at most one fill per track (a concurrent client-driven
// fill wins and the warm is skipped), and warmSem bounds warms server-wide:
// beyond the budget the warm is dropped, not queued — the next windowed miss
// re-attempts it. A failed warm puts the identity in a cooldown so repeated
// misses do not each spawn a fresh full-track remote read. A nil receiver is a
// no-op.
func (c *SubtitleCache) WarmInBackground(opts StreamExtractOpts, extract SUPExtractFunc, requestID string) {
	if c == nil || extract == nil {
		return
	}
	key, keyOK := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !keyOK {
		return
	}
	if relayDemuxRequestPathShedding(opts.InputPath, subtitleWarmRequestShedQuietWindow) {
		slog.Debug("subtitle cache warm skipped: request-path admission is shedding",
			"input", opts.InputPath, "track", opts.TrackIndex)
		return
	}
	if !c.warmAdmitted(key, time.Now()) {
		slog.Debug("subtitle cache warm skipped: failure cooldown",
			"input", opts.InputPath, "track", opts.TrackIndex)
		return
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("subtitle cache warm skipped: all warm slots busy",
			"input", opts.InputPath, "track", opts.TrackIndex)
		return
	}
	fill, _ := c.reserveFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP, true)
	if fill == nil {
		// Another fill (client-driven or a previous warm) is already in
		// flight, or the cache is unusable — either way, nothing to do.
		<-c.warmSem
		return
	}
	c.mu.Lock()
	fill.background = true
	c.mu.Unlock()

	// Full-track options: the warm ignores the triggering request's window
	// and writes only to the cache temp file (no response writer).
	opts.SeekSeconds = 0
	opts.DurationSeconds = 0
	opts.WindowRequested = false
	opts.AllowWindow = false
	opts.InputIsExtractedSup = false
	counter := &subtitleCountingWriter{w: fill.Tee(io.Discard)}
	opts.Writer = counter

	go func() {
		defer func() { <-c.warmSem }()
		ctx, cancel := context.WithTimeout(context.Background(), subtitleCacheWarmTimeout)
		defer cancel()

		start := time.Now()
		slog.Info("subtitle cache warm started",
			"input", opts.InputPath, "track", opts.TrackIndex)
		// A request-path shed that started after this warm was admitted means
		// the pipe is now serving viewers at its limit; yield the fill rather
		// than adding a full-track demux to it.
		if relayDemuxRequestPathShedding(opts.InputPath, subtitleWarmRequestShedQuietWindow) {
			fill.Discard()
			slog.DebugContext(ctx, "subtitle cache warm skipped: request-path admission is shedding",
				"input", opts.InputPath, "track", opts.TrackIndex)
			return
		}
		// A warm is opportunistic: when the relay is already at its demux
		// ceiling it is dropped rather than queued, and the next windowed miss
		// re-attempts it. Queueing would make it contend with the window a
		// client is waiting on, which is exactly the pileup that made warm
		// demuxes (and their layout probes) time out.
		warmCtx, releaseRelayDemux, demuxErr := acquireRelayDemuxRead(warmRelayDemuxContext(ctx, opts), opts.InputPath)
		if demuxErr != nil {
			fill.Discard()
			slog.DebugContext(ctx, "subtitle cache warm skipped: relay source saturated",
				"input", opts.InputPath, "track", opts.TrackIndex)
			return
		}
		defer releaseRelayDemux()
		ctx = warmCtx
		if err := extract(ctx, opts); err != nil {
			fill.fail(err)
			if errors.Is(err, ErrSubtitleWarmCanceled) {
				// The caller intentionally abandoned this opportunistic warm,
				// usually because playback resumed. It is not a source failure
				// and must not arm the failure cooldown. Generic context
				// cancellation still counts as a failure so a vanished relay
				// cannot trigger a re-spawn storm.
				slog.DebugContext(ctx, "subtitle cache warm canceled by playback")
				return
			}
			c.noteWarmOutcome(key, false, time.Now())
			slog.Log(ctx, slog.LevelWarn, "subtitle cache warm failed",
				subtitleTraceAttrsForRequest(requestID, opts.SourceCodec, SubtitleTracePhaseWarm, counter.bytes, subtitleTraceOutcome(ctx, err), time.Since(start),
					"input", opts.InputPath, "track", opts.TrackIndex, "error", err)...)
			return
		}
		if err := fill.Commit(); err != nil {
			c.noteWarmOutcome(key, false, time.Now())
			slog.Log(ctx, slog.LevelWarn, "subtitle cache warm commit failed",
				subtitleTraceAttrsForRequest(requestID, opts.SourceCodec, SubtitleTracePhaseWarm, counter.bytes, SubtitleTraceOutcomeCommitFailed, time.Since(start),
					"input", opts.InputPath, "track", opts.TrackIndex, "error", err)...)
			return
		}
		c.noteWarmOutcome(key, true, time.Now())
		slog.Log(ctx, slog.LevelInfo, "subtitle cache warm finished",
			subtitleTraceAttrsForRequest(requestID, opts.SourceCodec, SubtitleTracePhaseWarm, counter.bytes, SubtitleTraceOutcomeCommitted, time.Since(start),
				"input", opts.InputPath, "track", opts.TrackIndex)...)
	}()
}

// WarmTrackInBackground starts a detached full-track extract for one subtitle
// track in any output format (text VTT/ASS or bitmap .sup), so the first
// client fetch — full-track or windowed — hits the cache instead of paying a
// full remote demux. The returned channel closes exactly once on every path:
// when the warm finished (success or failure), and when it was skipped (warm
// slots busy, another fill in flight, cache disabled, already cached, or the
// identity is in its failure cooldown). Callers use it to release a
// request-scoped relay registration the warm held open. requestID is the
// triggering request's ID, captured at the boundary because the detached
// context no longer carries it; it is threaded into the warm's traces so they
// join that request.
//
// A warm whose extract fails is not retried immediately: noteWarmOutcome puts
// the identity in an exponentially growing cooldown, so repeated windowed
// misses do not each spawn a fresh full-track remote read (bandwidth
// amplification). The cooldown clears on the next successful commit.
//
// A warm yields the shared relay pipe to an admitted page extract on the same
// track (see beginPageFlight): the page is what the viewer is waiting on, so
// the running extract is canceled with ErrSubtitleWarmCanceled, the partial
// fill is discarded, and the warm resumes once the page settles. A yield never
// arms the failure cooldown. The warm abandons once pages have interrupted it
// subtitleWarmPageYieldAttempts times, or after subtitleWarmPageYieldWait
// without a gap; the next windowed miss re-kicks it.
//
// AllowWarmAfterPageRefusal clears the failed-warm cooldown for one track so the
// single recovery fill a refused bitmap page is waiting on may start. A page that
// a previous fill could not reach has already armed that cooldown for this
// identity; honoring it would leave the page with nothing to be served from, and
// the refusal and the retry would repeat forever. The page's own wait ledger
// (see subtitle_page_recovery.go) is what keeps this to one fill per page, and
// the ordinary fill reservation still single-flights it against every other warm
// for the same track.
func (c *SubtitleCache) AllowWarmAfterPageRefusal(opts StreamExtractOpts) {
	if c == nil {
		return
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		return
	}
	c.warmMu.Lock()
	delete(c.warmGuard, key)
	c.warmMu.Unlock()
}

// Staleness for identity-keyed (virtual) sources follows the same generation
// bucket as every other cache lookup: an entry committed under an
// earlier bucket is not found by later lookups, so a warm that loses its
// bucket race is simply re-kicked by the next windowed miss.
func (c *SubtitleCache) WarmTrackInBackground(opts StreamExtractOpts, extract SUPExtractFunc, requestID string) <-chan struct{} {
	return c.WarmTrackInBackgroundWithin(opts, extract, requestID, 0)
}

// WarmTrackInBackgroundWithin is WarmTrackInBackground with an optional bound on
// how long the fill may run. A zero budget keeps the cache's generous warm
// timeout. A fill something is waiting on (a bitmap page whose progressive
// artifact is the only thing that can answer it) passes the waiter's own budget:
// a fill that outlives the wait can never serve it, and holding a warm slot for
// it only delays the next one. The fill then ends with an `abandoned` line whose
// reason the page path records, instead of being canceled silently when the
// session ends.
func (c *SubtitleCache) WarmTrackInBackgroundWithin(opts StreamExtractOpts, extract SUPExtractFunc, requestID string, budget time.Duration) <-chan struct{} {
	done := make(chan struct{})
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	if c == nil || extract == nil || c.dir() == "" || format == "" {
		close(done)
		return done
	}
	// Already committed under the current bucket: nothing to warm.
	if _, _, ok := c.cachedFormatEntryPath(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format); ok {
		close(done)
		return done
	}
	key, keyOK := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !keyOK {
		close(done)
		return done
	}
	if relayDemuxRequestPathShedding(opts.InputPath, subtitleWarmRequestShedQuietWindow) && !opts.WaitingRelayDemux {
		slog.Debug("subtitle cache warm skipped: request-path admission is shedding",
			"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
		close(done)
		return done
	}
	if !c.warmAdmitted(key, time.Now()) {
		slog.Debug("subtitle cache warm skipped: failure cooldown",
			"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
		close(done)
		return done
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("subtitle cache warm skipped: all warm slots busy",
			"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
		close(done)
		return done
	}
	fill, inflight := c.reserveFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format, true)
	if fill == nil {
		<-c.warmSem
		// A fill already owns this track's artifact key. That is the healthy
		// case — the request path is producing the artifact this warm exists
		// for — so the warm adds nothing and must not queue behind it. Say so
		// instead of returning silently: an admitted-but-idle warm with no log
		// is indistinguishable from a broken one (production: 17 kicks, zero
		// admissions, zero skip-reason lines).
		if inflight != nil {
			slog.Info("subtitle cache warm skipped: a fill already owns this track",
				"input", opts.InputPath, "track", opts.TrackIndex, "format", format,
				"background_fill", c.fillStartedInBackground(inflight))
		} else {
			slog.Debug("subtitle cache warm skipped: cache unavailable for this source",
				"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
		}
		close(done)
		return done
	}
	c.mu.Lock()
	fill.background = true
	c.mu.Unlock()

	// Full-track options: the warm ignores the triggering request's window
	// and writes only to the cache temp file (no response writer).
	opts.SeekSeconds = 0
	opts.DurationSeconds = 0
	opts.WindowRequested = false
	opts.AllowWindow = false
	opts.InputIsExtractedSup = false
	opts.InputIsExtractedText = ""
	warmWriter := fill.Tee(io.Discard)
	// ASS is line-oriented and small; retain the warm's output as it streams
	// so windows whose range it has already passed are served from memory
	// instead of opening another relay read. Registered before the extract
	// starts so an early window can find it.
	var warmText *subtitleWarmTextProgress
	warmTextKey := ""
	if format == subtitleFormatASS {
		warmTextKey, _ = c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
		warmText = c.beginWarmTextProgress(warmTextKey)
		if warmText != nil {
			warmWriter = &subtitleWarmTextTap{dst: warmWriter, progress: warmText}
		}
	}
	counter := &subtitleCountingWriter{w: warmWriter}
	opts.Writer = counter

	go c.runWarmFill(subtitleWarmFill{
		opts:        opts,
		extract:     extract,
		requestID:   requestID,
		key:         key,
		format:      format,
		budget:      budget,
		fill:        fill,
		counter:     counter,
		warmText:    warmText,
		warmTextKey: warmTextKey,
		done:        done,
	})
	return done
}

// subtitleWarmFill is the mutable state of one admitted warm fill. It is a
// value because a page preemption rebuilds the fill under the same key and the
// next attempt continues with the rebuilt writers.
type subtitleWarmFill struct {
	opts        StreamExtractOpts
	extract     SUPExtractFunc
	requestID   string
	key         string
	format      string
	budget      time.Duration
	fill        *SubtitleCacheFill
	counter     *subtitleCountingWriter
	warmText    *subtitleWarmTextProgress
	warmTextKey string
	startedAt   time.Time
	done        chan struct{}
}

// Fill outcome and end reasons. They are tokens: the log line carries them for
// an operator, and the page ledger reads the final ones.
const (
	subtitleFillOutcomeCommitted = "committed"
	subtitleFillOutcomeFailed    = "failed"
	subtitleFillOutcomeAbandoned = "abandoned"

	subtitleFillEndUnknown         = "unknown"
	subtitleFillEndRequestShed     = "request_path_shedding"
	subtitleFillEndPageOwnsThePipe = "page_owns_the_pipe"
	subtitleFillEndPageYields      = "page_yields_exhausted"
	subtitleFillEndRekeyLost       = "rekey_lost_the_fill"
	subtitleFillEndRelaySaturated  = "relay_source_saturated"
	subtitleFillEndCanceled        = "canceled"
	subtitleFillEndExtractError    = "extract_error"
	subtitleFillEndCommitError     = "commit_error"
)

// runWarmFill executes one admitted fill. It always ends it in a definite state
// — committed, failed, or abandoned with a reason — and records the
// non-committing ones so a page waiting on the fill can act on its end.
func (c *SubtitleCache) runWarmFill(st subtitleWarmFill) {
	defer close(st.done)
	defer func() { <-c.warmSem }()
	defer func() { c.endWarmTextProgress(st.warmTextKey, st.warmText) }()
	budget := subtitleCacheWarmTimeout
	if st.budget > 0 && st.budget < budget {
		budget = st.budget
	}
	warmDeadlineCtx, cancelWarmDeadline := context.WithTimeout(context.Background(), budget)
	defer cancelWarmDeadline()

	start := time.Now()
	st.startedAt = start
	outcome, reason := subtitleFillOutcomeAbandoned, subtitleFillEndUnknown
	defer func() {
		if outcome == subtitleFillOutcomeCommitted {
			return
		}
		// Every non-committing end is recorded for the page path. Several of
		// these paths (yields exhausted, a re-key loss, a page budget spent
		// while the session held) used to end with no trace at all, and a page
		// waiting on the fill looped on `page_not_ready` until its own budget
		// ran out.
		c.noteFillEnded(st.key, reason, time.Now())
		if outcome == subtitleFillOutcomeFailed {
			return // its own WARN already ran
		}
		slog.InfoContext(context.Background(), "subtitle cache warm abandoned",
			"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format,
			"reason", reason, "elapsed", time.Since(start), "bytes", st.counter.bytes)
	}()

	slog.Info("subtitle cache warm started",
		"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format)
	outcome, reason = c.warmFillLoop(warmDeadlineCtx, &st, budget)
}

// warmFillLoop runs attempts until the fill settles. Detached warms skip a
// saturated relay source instead of queueing behind the request-path reads they
// exist to protect; the next windowed miss re-attempts them.
func (c *SubtitleCache) warmFillLoop(deadlineCtx context.Context, st *subtitleWarmFill, budget time.Duration) (string, string) {
	yields := 0
	for {
		// A request-path shed while this warm was settling means viewers are
		// already waiting at the pipe's limit. Drop the opportunistic full-track
		// read; the next windowed miss re-attempts it once the source stops
		// shedding.
		if relayDemuxRequestPathShedding(st.opts.InputPath, subtitleWarmRequestShedQuietWindow) && !st.opts.WaitingRelayDemux {
			st.fill.Discard()
			slog.DebugContext(deadlineCtx, "subtitle cache warm skipped: request-path admission is shedding",
				"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format)
			return subtitleFillOutcomeAbandoned, subtitleFillEndRequestShed
		}
		// An admitted page owns the pipe; wait for it (bounded) instead of
		// demuxing the same source alongside it. This also covers the race where
		// a page is admitted between our start and the first read.
		if !c.awaitPageSettled(st.key, subtitleWarmPageYieldWait) {
			st.fill.Discard()
			slog.DebugContext(deadlineCtx, "subtitle cache warm yielded to admitted page extracts",
				"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format)
			return subtitleFillOutcomeAbandoned, subtitleFillEndPageOwnsThePipe
		}
		ctx, cancel := context.WithCancelCause(deadlineCtx)
		run := c.registerWarmRun(st.key, cancel)
		if run == nil {
			cancel(nil)
			// A page was admitted between the settle wait and registration.
			yields++
			if yields >= subtitleWarmPageYieldAttempts {
				st.fill.Discard()
				return subtitleFillOutcomeAbandoned, subtitleFillEndPageYields
			}
			continue
		}
		warmCtx, releaseRelayDemux, demuxErr := acquireRelayDemuxRead(warmRelayDemuxContext(ctx, st.opts), st.opts.InputPath)
		ctx = warmCtx
		var err error
		if demuxErr == nil {
			err = st.extract(ctx, st.opts)
		}
		preempted := errors.Is(context.Cause(ctx), ErrSubtitleWarmCanceled)
		releaseRelayDemux()
		c.unregisterWarmRun(st.key, run)
		cancel(nil)

		outcome, reason, again := c.settleWarmAttempt(ctx, st, err, demuxErr, preempted, budget, deadlineCtx)
		if !again {
			return outcome, reason
		}
		yields++
		if yields >= subtitleWarmPageYieldAttempts {
			st.fill.Discard()
			return subtitleFillOutcomeAbandoned, subtitleFillEndPageYields
		}
	}
}

// settleWarmAttempt classifies one finished attempt. again reports that the fill
// was preempted by an admitted page and must continue once that page settles.
func (c *SubtitleCache) settleWarmAttempt(
	ctx context.Context,
	st *subtitleWarmFill,
	err, demuxErr error,
	preempted bool,
	budget time.Duration,
	deadlineCtx context.Context,
) (outcome, reason string, again bool) {
	if preempted && err != nil {
		// An admitted page took the pipe. This is a deliberate yield, never a
		// source failure: discard the partial fill, rebuild it under the same
		// key, and resume once the page settles. A non-cooperative extract that
		// ignored the cancel and returned a complete track (err == nil) falls
		// through and commits instead of repeating the whole read.
		newFill, newWarmText, newCounter := c.rebuildWarmFill(&st.opts, st.format, st.fill, st.warmText, st.warmTextKey)
		if newFill == nil {
			// Another fill now owns the key. warmText stays as it is, so the
			// caller's deferred endWarmTextProgress releases the old prefix.
			return subtitleFillOutcomeAbandoned, subtitleFillEndRekeyLost, false
		}
		st.fill, st.warmText, st.counter = newFill, newWarmText, newCounter
		slog.DebugContext(ctx, "subtitle cache warm yielded to page extract",
			"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format)
		return "", "", true
	}
	if demuxErr != nil {
		st.fill.Discard()
		slog.DebugContext(ctx, "subtitle cache warm skipped: relay source saturated",
			"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format)
		return subtitleFillOutcomeAbandoned, subtitleFillEndRelaySaturated, false
	}
	if err != nil {
		// A deliberate stop is not a source failure: the caller canceled it
		// (playback resumed, the playhead jumped), or the budget its waiter gave
		// it ran out. Discarding arms no cooldown, and both are recorded so a
		// waiting page can act on the end.
		budgetSpent := budget > 0 && errors.Is(deadlineCtx.Err(), context.DeadlineExceeded)
		if errors.Is(err, ErrSubtitleWarmCanceled) || budgetSpent {
			st.fill.Discard()
			why := subtitleFillEndCanceled
			if budgetSpent {
				// The fill outlived the page's own wait. Repeating the same read
				// cannot reach the page sooner, so this end is the page's final
				// answer.
				why = subtitleFillEndPageBudgetSpent
			}
			slog.DebugContext(ctx, "subtitle cache warm canceled by its caller",
				"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format, "reason", why)
			return subtitleFillOutcomeAbandoned, why, false
		}
		st.fill.fail(err)
		c.noteWarmOutcome(st.key, false, time.Now())
		slog.Log(ctx, slog.LevelWarn, "subtitle cache warm failed",
			subtitleTraceAttrsForRequest(st.requestID, st.opts.SourceCodec, SubtitleTracePhaseWarm, st.counter.bytes, subtitleTraceOutcome(ctx, err), time.Since(st.startedAt),
				"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format, "error", err)...)
		return subtitleFillOutcomeFailed, subtitleFillEndExtractError, false
	}
	if err := st.fill.Commit(); err != nil {
		c.noteWarmOutcome(st.key, false, time.Now())
		slog.Log(ctx, slog.LevelWarn, "subtitle cache warm commit failed",
			subtitleTraceAttrsForRequest(st.requestID, st.opts.SourceCodec, SubtitleTracePhaseWarm, st.counter.bytes, SubtitleTraceOutcomeCommitFailed, time.Since(st.startedAt),
				"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format, "error", err)...)
		return subtitleFillOutcomeFailed, subtitleFillEndCommitError, false
	}
	c.noteWarmOutcome(st.key, true, time.Now())
	slog.Log(ctx, slog.LevelInfo, "subtitle cache warm finished",
		subtitleTraceAttrsForRequest(st.requestID, st.opts.SourceCodec, SubtitleTracePhaseWarm, st.counter.bytes, SubtitleTraceOutcomeCommitted, time.Since(st.startedAt),
			"input", st.opts.InputPath, "track", st.opts.TrackIndex, "format", st.format)...)
	return subtitleFillOutcomeCommitted, "", false
}

// noteFillEnded records that this track's fill stopped without committing, and
// why. The page path reads it to answer finally instead of retrying a page no
// fill is moving toward.
func (c *SubtitleCache) noteFillEnded(key, reason string, at time.Time) {
	if c == nil || key == "" {
		return
	}
	c.fillEndMu.Lock()
	defer c.fillEndMu.Unlock()
	if c.fillEnds == nil {
		c.fillEnds = make(map[string]subtitleFillEnd)
	}
	if len(c.fillEnds) >= subtitleFillEndsMaxEntries {
		for oldest := range c.fillEnds {
			delete(c.fillEnds, oldest)
			break
		}
	}
	c.fillEnds[key] = subtitleFillEnd{at: at, reason: reason}
}

// fillEndedSince reports the reason this track's fill ended without committing
// at or after since, or "" when no such end was recorded.
func (c *SubtitleCache) fillEndedSince(key string, since time.Time) string {
	if c == nil || key == "" {
		return ""
	}
	c.fillEndMu.Lock()
	defer c.fillEndMu.Unlock()
	end, ok := c.fillEnds[key]
	if !ok || end.at.Before(since) {
		return ""
	}
	return end.reason
}

// rebuildWarmFill replaces a warm fill that was discarded after a page
// preemption. It re-reserves the same track key, restarts the ASS prefix tap,
// and returns the new fill with the writers/counter the caller must use for
// the next attempt. A nil fill means another fill now owns the key (or the
// cache is unusable), so the warm must stop rather than duplicate the work.
//
// The reserve re-derives the key from cacheIdentity, so a fill that lands
// across a generation-bucket roll commits under the new bucket while the
// caller's admission/flight key stays on the old one; the next windowed miss
// then re-kicks the warm under the new key.
func (c *SubtitleCache) rebuildWarmFill(
	opts *StreamExtractOpts,
	format string,
	oldFill *SubtitleCacheFill,
	warmText *subtitleWarmTextProgress,
	warmTextKey string,
) (*SubtitleCacheFill, *subtitleWarmTextProgress, *subtitleCountingWriter) {
	oldFill.Discard()
	fill, _ := c.reserveFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format, true)
	if fill == nil {
		return nil, nil, nil
	}
	c.mu.Lock()
	fill.background = true
	c.mu.Unlock()
	var warmWriter = fill.Tee(io.Discard)
	if format == subtitleFormatASS {
		c.endWarmTextProgress(warmTextKey, warmText)
		warmText = c.beginWarmTextProgress(warmTextKey)
		if warmText != nil {
			warmWriter = &subtitleWarmTextTap{dst: warmWriter, progress: warmText}
		}
	}
	counter := &subtitleCountingWriter{w: warmWriter}
	opts.Writer = counter
	return fill, warmText, counter
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

func subtitleCacheFormatKey(inputPath string, trackIndex int, mtime time.Time, size int64, format string) string {
	return fmt.Sprintf("%s%d-%d.%s", subtitleCacheKeyPrefix(inputPath, trackIndex), mtime.UnixNano(), size, format)
}

// Lookup opens the cached full-track .sup extract for the given source file
// and subtitle stream ordinal. The source is stat'ed on every lookup: an
// mtime or size mismatch means the entry (if any) is stale and reads as a
// miss. On a hit the returned modTime is the *source* file's mtime — stable
// across hits, suitable for Last-Modified — while the cache file's own mtime
// is bumped to record recency for LRU eviction. The caller owns closing the
// returned file.
func (c *SubtitleCache) Lookup(inputPath string, trackIndex int) (f *os.File, modTime time.Time, ok bool) {
	return c.lookup(inputPath, "", trackIndex, subtitleFormatSUP)
}

func (c *SubtitleCache) lookup(inputPath string, args ...interface{}) (f *os.File, modTime time.Time, ok bool) {
	var cacheIdentity, format string
	var trackIndex int
	if len(args) == 3 {
		cacheIdentity, _ = args[0].(string)
		trackIndex, _ = args[1].(int)
		format, _ = args[2].(string) //nolint:gosec // The index is guarded by len(args) == 3 above.
	} else if len(args) == 2 {
		trackIndex, _ = args[0].(int)
		format, _ = args[1].(string)
	}
	path, modTime, ok := c.cachedFormatEntryPath(inputPath, cacheIdentity, trackIndex, format)
	if !ok {
		return nil, time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	return f, modTime, true
}

// LookupText returns a complete cached text subtitle artifact. The cache key
// includes the source path, mtime, size, stream ordinal, and output format.
func (c *SubtitleCache) LookupText(inputPath string, trackIndex int, format string) ([]byte, bool) {
	format = normalizeCachedTextSubtitleFormat(format)
	if format == "" {
		return nil, false
	}
	f, _, ok := c.lookup(inputPath, "", trackIndex, format)
	if !ok {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	return data, err == nil
}

// LookupWebVTT returns the complete WebVTT rendition native playback cached
// for a text track (ServeExtract stores SubRip and forced-VTT extracts under
// the vtt key), so another reader can reuse it instead of demuxing the source.
func (c *SubtitleCache) LookupWebVTT(inputPath string, trackIndex int) ([]byte, bool) {
	f, _, ok := c.lookup(inputPath, "", trackIndex, SubtitleFormatVTTV3)
	if !ok {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	return data, err == nil
}

// ExtractText reuses a complete cached text track or extracts it once for this
// request. The fill captures source identity before extraction, so a replaced
// source cannot publish an old extract under the new file's cache key.
func (c *SubtitleCache) ExtractText(ctx context.Context, inputPath string, trackIndex int, format string, extract func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	format = normalizeCachedTextSubtitleFormat(format)
	if format == "" {
		return nil, fmt.Errorf("unsupported cached subtitle format")
	}
	// Share another request's extraction rather than demuxing the source a
	// second time: look the track up, else reserve the fill, else wait for
	// whoever holds it and look again. A fill that commits between a busy
	// reservation and the wait is found by the next lookup, and one that
	// commits during the last wait by the lookup after the loop. The rounds
	// are bounded, so a cache that can't take a fill (disabled, unwritable)
	// falls through to extracting without one.
	var fill *SubtitleCacheFill
	for round := 0; round < textFillRounds && fill == nil; round++ {
		if data, ok := c.LookupText(inputPath, trackIndex, format); ok {
			return data, nil
		}
		if fill = c.beginFill(inputPath, "", trackIndex, format); fill != nil {
			break
		}
		if afterBusyTextFill != nil {
			afterBusyTextFill()
		}
		c.waitForTextFill(ctx, inputPath, TextSubtitleTrack{Ordinal: trackIndex, Format: format})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	// Another filler may have committed since the last lookup: between it
	// and this reservation, or during the last round's wait. Reuse its entry
	// rather than extracting again.
	if data, ok := c.LookupText(inputPath, trackIndex, format); ok {
		slog.Log(ctx, slog.LevelDebug, "subtitle text served from cache",
			subtitleTraceAttrs(ctx, "", SubtitleTracePhaseServe, int64(len(data)), SubtitleTraceOutcomeCacheHit, -1,
				"track", trackIndex, "format", format)...)
		if fill != nil {
			fill.Discard()
		}
		return data, nil
	}
	data, err := extract(ctx)
	if err != nil {
		if fill != nil {
			fill.Discard()
		}
		return nil, err
	}
	if fill != nil {
		// Tee records cache write failures; its destination io.Discard cannot fail.
		_, _ = fill.Tee(io.Discard).Write(data)
		if err := fill.Commit(); err != nil {
			slog.Log(ctx, slog.LevelWarn, "subtitle cache commit failed",
				subtitleTraceAttrs(ctx, "", SubtitleTracePhaseWarm, int64(len(data)), SubtitleTraceOutcomeCommitFailed, -1,
					"track", trackIndex, "format", format, "error", err)...)
		}
	}
	return data, nil
}

// textFillRounds bounds ExtractText's lookup, reserve and wait rounds.
const textFillRounds = 3

// afterBusyTextFill runs when ExtractText finds another fill in progress,
// before it waits; tests use it to finish that fill in between.
var afterBusyTextFill func()

// afterTextFillWaitCapture runs once waitForTextFill holds an in-flight
// fill's done channel, before it blocks on it; tests use it to act while a
// request is waiting.
var afterTextFillWaitCapture func()

func normalizeCachedTextSubtitleFormat(format string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), ".")) {
	case subtitleFormatSRT, subtitleCodecSubRip:
		return subtitleFormatSRT
	case subtitleFormatASS, subtitleCodecSSA:
		return subtitleFormatASS
	default:
		return ""
	}
}

// cachedEntryPath reports whether a committed entry exists for the given
// source+track and returns its path plus the source file's mtime. Like
// Lookup it stats the source on every call (a changed source reads as a
// miss) and bumps the entry's mtime to record recency for LRU eviction.
// Callers that hand the path to an external reader (ffmpeg) rather than
// opening it themselves use this instead of Lookup.
func (c *SubtitleCache) cachedEntryPath(inputPath, cacheIdentity string, trackIndex int) (path string, srcModTime time.Time, ok bool) {
	return c.cachedFormatEntryPath(inputPath, cacheIdentity, trackIndex, subtitleFormatSUP)
}

func (c *SubtitleCache) cachedFormatEntryPath(inputPath, cacheIdentity string, trackIndex int, format string) (path string, srcModTime time.Time, ok bool) {
	dir := c.dir()
	if dir == "" {
		return "", time.Time{}, false
	}
	identity, modTime, size, ok := subtitleCacheSource(inputPath, cacheIdentity, time.Now())
	if !ok {
		return "", time.Time{}, false
	}
	path = filepath.Join(dir, subtitleCacheFormatKey(identity, trackIndex, modTime, size, format))
	if _, err := os.Stat(path); err != nil {
		return "", time.Time{}, false
	}
	// Recency bump for LRU. Best-effort: a failure (e.g. read-only remount)
	// only degrades eviction ordering, not correctness.
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		slog.Debug("subtitle cache recency bump failed", "path", path, "error", err)
	}
	return path, modTime, true
}

// SubtitleCacheFill is an in-progress cache population for one track. Bytes
// are written to a temp file via the writer returned by Tee; Commit renames
// it into place atomically, Discard throws it away. Exactly one of Commit or
// Discard must be called.
type SubtitleCacheFill struct {
	c             *SubtitleCache
	key           string
	inputPath     string
	identity      string
	cacheIdentity string
	trackIndex    int
	srcMtime      time.Time
	srcSize       int64
	tmp           *os.File
	// progressive marks a fill whose extract streams through Tee, so its
	// temp file grows as the extract runs and requests can watch it. A text
	// batch writes its files directly and only at the end.
	progressive bool
	// sup indexes the display sets of a bitmap fill's growing artifact, so a
	// bounded window inside what it has parsed is served from the artifact
	// instead of another source read. Nil for text formats.
	sup *supCoverage
	// background marks a fill owned by a detached background warm rather than
	// a client request. It exists only so the serve path can count a page
	// extract that runs alongside the warm's demux on the same relay pipe; no
	// scheduling decision reads it. Guarded by the cache's mu.
	background bool
	// done closes exactly once when the fill settles (Commit or Discard),
	// releasing any request that is waiting to serve the committed entry.
	done chan struct{}

	// mu guards the fields below and the temp file: watchers read it under
	// mu, and Commit and Discard close it under mu.
	mu sync.Mutex
	// failed flips when a temp-file write errors (e.g. disk full); the tee
	// keeps serving its writer and Commit refuses to publish the entry.
	failed bool
	// written counts the bytes in the temp file.
	written int64
	// grew is closed and replaced whenever written grows or failed flips,
	// and closed for good when the fill ends.
	grew  chan struct{}
	ended bool
	// err is why a fill was discarded, for the requests watching it.
	err error
	// viewers counts the requests watching the fill.
	viewers int
	// orphaned runs under mu when the last viewer leaves before the fill
	// ends; it reports whether the extract carries on without viewers.
	orphaned  func() bool
	abandoned bool
}

// errSubtitleSourceChanged discards a fill whose source changed during the
// extract: its bytes belong to neither version of the file.
var errSubtitleSourceChanged = errors.New("source file changed during extract; cache fill discarded")

// BeginFill reserves the in-flight slot for the given track and creates the
// temp file the tee will write into. Returns nil — meaning "stream without
// caching" — when caching is disabled, the source can't be stat'ed, the
// cache directory can't be created, or another fill for the same track is
// already in flight. Callers that want to wait for a competing fill and serve
// its committed entry use beginFillOrWait.
func (c *SubtitleCache) BeginFill(inputPath string, trackIndex int) *SubtitleCacheFill {
	return c.beginFill(inputPath, "", trackIndex, subtitleFormatSUP)
}

// beginFill is beginFillOrWait for callers that only want to lead a fill and
// skip when another is already in flight (background warms).
func (c *SubtitleCache) beginFill(inputPath, cacheIdentity string, trackIndex int, format string) *SubtitleCacheFill {
	fill, _ := c.reserveFill(inputPath, cacheIdentity, trackIndex, format, false)
	return fill
}

// beginFillOrWait reserves the in-flight slot for the given track or reports
// the fill that already holds it. Exactly one of the return values is
// meaningful:
//
//   - fill != nil: the caller owns the reservation and must Commit or Discard it.
//   - fill == nil && wait != nil: another fill for the same key is in flight.
//     wait closes when that fill commits or is discarded; waiting on it and
//     then re-reading the cache lets a request serve the committed entry
//     instead of running a duplicate full demux.
//   - fill == nil && wait == nil: caching is unavailable (disabled, source
//     cannot be stat'ed, or the cache directory cannot be created). The caller
//     streams uncached.
func (c *SubtitleCache) beginFillOrWait(inputPath, cacheIdentity string, trackIndex int, format string) (*SubtitleCacheFill, <-chan struct{}) {
	fill, busy := c.reserveFill(inputPath, cacheIdentity, trackIndex, format, true)
	if fill != nil {
		return fill, nil
	}
	if busy != nil {
		return nil, busy.done
	}
	return nil, nil
}

// reserveFill reserves the in-flight slot for a track and creates the fill's
// temp file. When another fill already holds the slot it returns that fill
// as inflight instead. Both are nil when caching is unavailable.
func (c *SubtitleCache) reserveFill(inputPath, cacheIdentity string, trackIndex int, format string, progressive bool) (fill, inflight *SubtitleCacheFill) {
	if c == nil {
		return nil, nil
	}
	dir := c.dir()
	if dir == "" {
		return nil, nil
	}
	identity, modTime, size, ok := subtitleCacheSource(inputPath, cacheIdentity, time.Now())
	if !ok {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("subtitle cache dir create failed", "dir", dir, "error", err)
		return nil, nil
	}
	fill = &SubtitleCacheFill{
		c:             c,
		key:           subtitleCacheFormatKey(identity, trackIndex, modTime, size, format),
		inputPath:     inputPath,
		identity:      identity,
		cacheIdentity: cacheIdentity,
		trackIndex:    trackIndex,
		srcMtime:      modTime,
		srcSize:       size,
		progressive:   progressive,
		done:          make(chan struct{}),
		grew:          make(chan struct{}),
	}
	if format == subtitleFormatSUP {
		// A bitmap fill's artifact is a .sup stream: index it as it grows so a
		// page inside the parsed range is a local read (see
		// subtitle_progressive_sup.go).
		fill.sup = &supCoverage{}
	}
	// Held until the temp file exists, so a request that finds the fill
	// cannot watch it before then.
	fill.mu.Lock()
	defer fill.mu.Unlock()
	c.mu.Lock()
	if busy := c.inflight[fill.key]; busy != nil {
		c.mu.Unlock()
		return nil, busy
	}
	c.inflight[fill.key] = fill
	c.mu.Unlock()

	tmp, err := os.CreateTemp(dir, fill.key+".part-*")
	if err != nil {
		fill.endLocked()
		c.release(fill)
		slog.Warn("subtitle cache temp create failed", "dir", dir, "error", err)
		return nil, nil
	}
	fill.tmp = tmp
	return fill, nil
}

// fillStartedInBackground reports whether an in-flight fill was admitted as a
// background (warm) fill rather than by the request path. It is observation
// only, for the warm's skip log.
func (c *SubtitleCache) fillStartedInBackground(fill *SubtitleCacheFill) bool {
	if c == nil || fill == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fill.background
}

// warmInFlight reports whether a background warm is currently filling the
// committed artifact for opts' identity. It is observation only: the serve
// path uses it to count a page extract that shares the relay pipe with the
// warm, never to choose a path or wait differently.
func (c *SubtitleCache) warmInFlight(opts StreamExtractOpts, format string) bool {
	if c == nil {
		return false
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fill := c.inflight[key]
	return fill != nil && fill.background
}

func subtitleCacheSource(inputPath, cacheIdentity string, now time.Time) (string, time.Time, int64, bool) {
	if identity := strings.TrimSpace(cacheIdentity); identity != "" {
		bucket := now.Unix() / int64(subtitleCacheGenerationBucket/time.Second)
		return identity, time.Unix(bucket*int64(subtitleCacheGenerationBucket/time.Second), 0), 0, true
	}
	src, err := os.Stat(inputPath)
	if err != nil {
		return "", time.Time{}, 0, false
	}
	return inputPath, src.ModTime(), src.Size(), true
}

// FontBundleKey is a stable identity for font-bundle caching. Virtual rows
// key on the release's durable provider identity, the row's probed subtitle
// inventory, and a generation bucket — not on the pinned result candidate or a
// resolved relay URL, both of which rotate per registration and would strand
// warmed bundles under keys requests never ask for.
//
// The inventory alone is not enough: a rotated release (or a re-grabbed file
// behind the same row) can keep an identical subtitle layout while shipping
// different attached fonts or an edited script, and the bundle would then be
// served from the previous release for the rest of the bucket. ReleaseIdentity
// is the provider's durable identity for the release, the same tiers
// boundRowSharesRelease compares, so a candidate rotation to different bytes
// re-keys while an unchanged release keeps its warmed bundle. An identity-empty
// row still keys by file id + inventory + bucket.
//
// Local rows key on the file row's size and mtime so a re-probed or replaced
// file reads as a miss.
type FontBundleKey struct {
	FileID          int
	Virtual         bool
	ReleaseIdentity string // durable provider identity; "" means unknown
	InventoryToken  string // probed subtitle inventory; "" is a stable layout token
	Size            int64
	MtimeUnixNano   int64
	FFmpegPath      string
}

// source resolves the stable cache identity and invalidation coordinates for
// the key, mirroring subtitleCacheSource's two modes. Local rows use the file
// row's mtime/size verbatim (no generation bucket); virtual rows key on the
// release identity and subtitle inventory plus a generation bucket so a
// rotated source can never be served past the bucket boundary. A virtual row
// without either identity still keys by file id + bucket. ok is false for rows
// that cannot be keyed reliably (a local row without a usable mtime/size),
// which callers treat as "do not cache" — the same fallback the DV RPU memo
// uses.
func (k FontBundleKey) source(now time.Time) (identity string, modTime time.Time, size int64, ok bool) {
	if k.Virtual {
		bucket := now.Unix() / int64(subtitleCacheGenerationBucket/time.Second)
		variant := k.ReleaseIdentity + "\x1f" + k.InventoryToken
		return fontBundleIdentity(k.FileID, variant, k.FFmpegPath, bucket),
			time.Unix(bucket*int64(subtitleCacheGenerationBucket/time.Second), 0), 0, true
	}
	if k.MtimeUnixNano == 0 || k.Size <= 0 {
		return "", time.Time{}, 0, false
	}
	return fontBundleIdentity(k.FileID, "", k.FFmpegPath, 0), time.Unix(0, k.MtimeUnixNano), k.Size, true
}

// fontBundleIdentity hashes the stable components of a font-bundle cache key.
// variant carries the virtual row's release identity and inventory token
// (empty for local rows). The ffmpeg path is included so a binary relocation
// (which can change extraction output) starts a fresh cache.
func fontBundleIdentity(fileID int, variant, ffmpegPath string, bucket int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%d", fileID, variant, ffmpegPath, bucket)))
	return fmt.Sprintf("%x", sum[:12])
}

func fontBundleCacheFileName(identity string, modTime time.Time, size int64) string {
	return fmt.Sprintf("fb-%s-%d-%d.%s", identity, modTime.UnixNano(), size, subtitleFormatFontBundle)
}

// VirtualSubtitleCacheIdentity is the credential-free, stable cache identity
// for a virtual subtitle extract. Relay URLs rotate per registration and would
// defeat the cache, so the identity is built from the pinned provider-neutral
// URI's "result=" candidate id plus the effective ffmpeg track ordinal (the
// ordinal the extraction will actually map, after any drift remap). Staleness
// is bounded by the same generation bucket subtitleCacheSource
// applies to identity-keyed entries.
//
// inventory is the token for the row's probed subtitle layout (see
// SubtitleInventoryToken). Folding it in is what invalidates the entry when the
// row's probed inventory changes: a re-probe that establishes a different
// layout names different streams under the same ordinals, so the artifact
// extracted against the previous inventory must not be served for the new one.
// A re-probe that observes the same layout produces the same token and keeps
// the warm entry.
//
// The hash is deliberately ordinal-based: folding in a container track id
// would change every key and orphan entries committed by earlier releases.
// Across a drift remap the start-path warm, which keys on the plan ordinal, can
// therefore miss the post-remap serve identity; the serve-path window warm
// abandons a fill whose own probe establishes a different identity and re-keys
// onto the post-remap ordinal before committing, so an orphaned plan-ordinal
// entry is never published and self-heals on the next window.
func VirtualSubtitleCacheIdentity(fileID int, virtualSourceURI string, trackIndex int, inventory ...string) string {
	pinned := ""
	if parsed, err := url.Parse(strings.TrimSpace(virtualSourceURI)); err == nil {
		pinned = strings.TrimSpace(parsed.Query().Get("result"))
	}
	inv := ""
	if len(inventory) > 0 {
		inv = inventory[0]
	}
	if inv != "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d|%s", fileID, pinned, trackIndex, inv)))
		return hex.EncodeToString(sum[:12])
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d", fileID, pinned, trackIndex)))
	return fmt.Sprintf("%x", sum[:12])
}

// SubtitleInventoryToken is a stable token for one row's probed subtitle
// inventory. It feeds VirtualSubtitleCacheIdentity so a layout change re-keys
// the cache, while an unchanged layout keeps its artifacts across ordinary
// re-probes.
//
// Only fields that identify the stream a given ordinal resolves to are
// included. Container track ids, titles, and display flags are excluded:
// remuxes and metadata edits rewrite them without changing which subtitle
// stream an ordinal names, and folding them in would discard every warm
// artifact for cosmetic changes.
func SubtitleInventoryToken(tracks []models.SubtitleTrack) string {
	if len(tracks) == 0 {
		return "none" //nolint:goconst // Conditional-playback sentinel; other "none" literals in this package mean different values
	}
	var b strings.Builder
	for _, track := range tracks {
		b.WriteString(strconv.Itoa(track.Index))
		b.WriteByte(':')
		b.WriteString(strings.ToLower(strings.TrimSpace(track.Codec)))
		b.WriteByte(':')
		b.WriteString(strings.ToLower(strings.TrimSpace(track.Language)))
		if track.Forced {
			b.WriteString(":forced")
		}
		if track.HearingImpaired {
			b.WriteString(":hi")
		}
		b.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// LookupFontBundle returns the cached encoded font-bundle JSON for the key, or
// false on a miss. A nil receiver reads as a miss so cache-less handlers keep
// their uncached path.
func (c *SubtitleCache) LookupFontBundle(key FontBundleKey) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	path, ok := c.fontBundleEntryPath(key, time.Now())
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// fontBundleFlightKey derives the stable in-flight/backoff key for a font
// bundle. The invalidation coordinates are part of the key, so concurrent
// requests for a changed source version (different mtime/size) neither
// coalesce nor share a failure backoff with the stale version.
func (c *SubtitleCache) fontBundleFlightKey(key FontBundleKey, now time.Time) (string, bool) {
	if c == nil {
		return "", false
	}
	identity, modTime, size, ok := key.source(now)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("fontbundle:%s-%d-%d", identity, modTime.UnixNano(), size), true
}

// FontBundleRetryAfter reports the remaining font-bundle backoff after a
// failed extraction. Handlers use it to serve the same pending empty bundle
// without spawning another ffprobe/ffmpeg for a flapping provider. ok is false
// when the key is unkeyable or the next attempt is allowed.
func (c *SubtitleCache) FontBundleRetryAfter(key FontBundleKey) (time.Duration, bool) {
	flightKey, ok := c.fontBundleFlightKey(key, time.Now())
	if !ok {
		return 0, false
	}
	now := time.Now()
	c.fontFailureMu.Lock()
	defer c.fontFailureMu.Unlock()
	retryAt, ok := c.fontFailureRetryAt[flightKey]
	if !ok {
		return 0, false
	}
	if !now.Before(retryAt) {
		delete(c.fontFailureRetryAt, flightKey)
		delete(c.fontFailureFallback, flightKey)
		return 0, false
	}
	return retryAt.Sub(now), true
}

func (c *SubtitleCache) noteFontBundleFailure(flightKey string, extractErr error) {
	if c == nil || flightKey == "" {
		return
	}
	c.fontFailureMu.Lock()
	defer c.fontFailureMu.Unlock()
	if c.fontFailureRetryAt == nil {
		c.fontFailureRetryAt = make(map[string]time.Time)
	}
	if errors.Is(extractErr, ErrSubtitleFontSubsetUnavailable) {
		if c.fontFailureFallback == nil {
			c.fontFailureFallback = make(map[string]string)
		}
		c.fontFailureFallback[flightKey] = SubtitleFontFallbackSubsetUnavailable
	}
	if len(c.fontFailureRetryAt) >= subtitleFontBundleFailureMaxEntries {
		now := time.Now()
		for key, retryAt := range c.fontFailureRetryAt {
			if !now.Before(retryAt) {
				delete(c.fontFailureRetryAt, key)
				delete(c.fontFailureFallback, key)
			}
		}
		if len(c.fontFailureRetryAt) >= subtitleFontBundleFailureMaxEntries {
			// Every entry is still live; evict the one closest to lapsing so
			// the map stays bounded under pathological churn.
			victim := ""
			var victimAt time.Time
			for key, retryAt := range c.fontFailureRetryAt {
				if victim == "" || retryAt.Before(victimAt) {
					victim, victimAt = key, retryAt
				}
			}
			delete(c.fontFailureRetryAt, victim)
			delete(c.fontFailureFallback, victim)
		}
	}
	c.fontFailureRetryAt[flightKey] = time.Now().Add(subtitleFontBundleFailureCooldown)
}

// FontBundleFallback reports the permanent fallback reason recorded for a key's
// last font-bundle extraction, or "" when the failure was transient or absent.
// It is meaningful only while the failure backoff is active; an expired entry
// is cleared like FontBundleRetryAfter does.
func (c *SubtitleCache) FontBundleFallback(key FontBundleKey) string {
	if c == nil {
		return ""
	}
	flightKey, ok := c.fontBundleFlightKey(key, time.Now())
	if !ok {
		return ""
	}
	c.fontFailureMu.Lock()
	defer c.fontFailureMu.Unlock()
	reason := c.fontFailureFallback[flightKey]
	if reason == "" {
		return ""
	}
	retryAt, cooling := c.fontFailureRetryAt[flightKey]
	if !cooling || !time.Now().Before(retryAt) {
		delete(c.fontFailureFallback, flightKey)
		return ""
	}
	return reason
}

func (c *SubtitleCache) noteFontBundleSuccess(flightKey string) {
	if c == nil || flightKey == "" {
		return
	}
	c.fontFailureMu.Lock()
	delete(c.fontFailureRetryAt, flightKey)
	delete(c.fontFailureFallback, flightKey)
	c.fontFailureMu.Unlock()
}

// startFontBundleFlight registers or joins the single-flight extraction for
// key and returns the caller's result channel. The extraction runs on its own
// goroutine (singleflight.DoChan) on a context detached from every caller and
// bounded by subtitleFontBundleExtractTimeout, so a canceled waiter never
// cancels the shared work. The bool is false when the key cannot be resolved
// to a stable cache identity, in which case the caller extracts uncached.
func (c *SubtitleCache) startFontBundleFlight(ctx context.Context, key FontBundleKey, extract func(context.Context) ([]byte, error)) (<-chan singleflight.Result, bool) {
	flightKey, ok := c.fontBundleFlightKey(key, time.Now())
	if !ok {
		return nil, false
	}
	ch := c.fontBundleFlight.DoChan(flightKey, func() (any, error) {
		// Another caller may have committed the entry while we waited to lead.
		if data, ok := c.LookupFontBundle(key); ok {
			c.noteFontBundleSuccess(flightKey)
			return data, nil
		}
		extractCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subtitleFontBundleExtractTimeout)
		defer cancel()
		data, extractErr := extract(extractCtx)
		if extractErr != nil {
			if errors.Is(extractErr, ErrRelayDemuxBusy) {
				// The relay is already at its demux ceiling. Fonts are optional
				// to playback, so this attempt is skipped without arming the
				// failure backoff: a waiter gets the pending (empty bundle)
				// shape and the next request retries once the pipe frees up.
				return nil, errSubtitleFontBundleFlightSkipped
			}
			c.noteFontBundleFailure(flightKey, extractErr)
			return nil, extractErr
		}
		if storeErr := c.storeFontBundle(key, data); storeErr != nil {
			slog.WarnContext(ctx, "subtitle font bundle cache store failed", "error", storeErr)
		}
		items := fontBundleItemCount(data)
		if items == 0 {
			// A completed extraction with no attachments is a definitive empty
			// bundle, not a failed one: the client should cache it and stop
			// polling. Say so explicitly so it is distinguishable from the
			// silent timeout shape below.
			slog.InfoContext(ctx, "subtitle font bundle stored with no attachments",
				"file_id", key.FileID, "bytes", len(data))
		} else {
			slog.DebugContext(ctx, "subtitle font bundle stored",
				"file_id", key.FileID, "items", items, "bytes", len(data))
		}
		c.noteFontBundleSuccess(flightKey)
		return data, nil
	})
	return ch, true
}

// fontBundleFlightOutcome converts a singleflight result into the cache API's
// (data, ready, err) shape. A timeout or cancellation is "pending", not an
// error: a client-facing caller degrades to an empty bundle while the
// detached flight keeps running. A definitive extractor failure (no usable
// attachment, malformed input) is surfaced so the caller can log it and
// degrade to the pending empty bundle instead of failing playback.
func fontBundleFlightOutcome(res singleflight.Result) ([]byte, bool, error) {
	if res.Err != nil {
		if errors.Is(res.Err, context.DeadlineExceeded) ||
			errors.Is(res.Err, context.Canceled) ||
			errors.Is(res.Err, errSubtitleFontBundleFlightSkipped) {
			return nil, false, nil
		}
		return nil, false, res.Err
	}
	data, _ := res.Val.([]byte)
	return data, true, nil
}

// fontBundleItemCount reports how many attachments an encoded bundle carries.
// The cache stores the handler's bare JSON array; an unparseable payload still
// has non-zero bytes, so callers can distinguish it from a definitive empty
// bundle by size.
func fontBundleItemCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		if len(bytes.TrimSpace(data)) == 0 {
			return 0
		}
		return -1
	}
	return len(items)
}

// ExtractFontBundle returns the encoded font-bundle JSON for the key, reusing
// a committed disk entry when present and otherwise running extract once under
// single-flight and writing the result through to disk for every waiter. The
// extraction runs detached from the leading request (bounded to
// subtitleFontBundleExtractTimeout), so a canceled leader does not fail the
// shared work other callers are waiting on. Callers that must not block on a
// cold extraction use ExtractFontBundleWithin instead.
func (c *SubtitleCache) ExtractFontBundle(ctx context.Context, key FontBundleKey, extract func(context.Context) ([]byte, error)) ([]byte, error) {
	if data, ok := c.LookupFontBundle(key); ok {
		return data, nil
	}
	if c == nil {
		return extract(ctx)
	}
	resultCh, ok := c.startFontBundleFlight(ctx, key, extract)
	if !ok {
		// Not reliably keyable (e.g. a local row without mtime/size): extract
		// uncached, matching the DV RPU memo's fallback.
		return extract(ctx)
	}
	data, ready, err := fontBundleFlightOutcome(<-resultCh)
	if err != nil {
		return nil, err
	}
	if !ready {
		// The shared flight timed out; the blocking API has no pending state.
		return nil, context.DeadlineExceeded
	}
	return data, nil
}

// ExtractFontBundleWithin serves a cached font bundle for key, or starts (or
// joins) the detached single-flight extraction and waits up to wait for it.
// It returns (data, true, nil) when a bundle is available, (nil, false, nil)
// when the extraction is still running — the caller should serve an empty
// bundle and let the flight land in the disk cache — or (nil, false, err) on
// a definitive extractor failure. A wait <= 0 still registers the flight but
// returns immediately, so concurrent requests coalesce instead of each
// starting their own extraction. A nil cache or an unkeyable local row falls
// back to a synchronous extract.
func (c *SubtitleCache) ExtractFontBundleWithin(ctx context.Context, key FontBundleKey, wait time.Duration, extract func(context.Context) ([]byte, error)) ([]byte, bool, error) {
	if data, ok := c.LookupFontBundle(key); ok {
		return data, true, nil
	}
	if c == nil {
		data, err := extract(ctx)
		return data, err == nil, err
	}
	resultCh, ok := c.startFontBundleFlight(ctx, key, extract)
	if !ok {
		data, err := extract(ctx)
		return data, err == nil, err
	}
	if wait <= 0 {
		return nil, false, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res := <-resultCh:
		return fontBundleFlightOutcome(res)
	case <-timer.C:
		return nil, false, nil
	case <-ctx.Done():
		// The caller gave up; the detached flight keeps running for the cache.
		return nil, false, nil
	}
}

// WarmFontBundleInBackground starts a detached, single-flighted font-bundle
// extraction for key so a later HTTP fetch hits the disk cache instead of
// paying the cold relay cost. It shares fontBundleFlight, the disk cache, and
// the extraction budget with ExtractFontBundleWithin, so a warm racing an HTTP
// request coalesces onto a single ffmpeg run. The returned channel closes when
// the warm settled (ran, failed, was skipped, or another fill held a warm
// slot); callers use it to release a request-scoped relay registration. A nil
// receiver or disabled cache is a no-op.
func (c *SubtitleCache) WarmFontBundleInBackground(key FontBundleKey, extract func(context.Context) ([]byte, error)) <-chan struct{} {
	done := make(chan struct{})
	if c == nil || extract == nil || c.dir() == "" {
		close(done)
		return done
	}
	if _, ok := c.LookupFontBundle(key); ok {
		close(done)
		return done
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("font bundle warm skipped: all warm slots busy", "file_id", key.FileID)
		close(done)
		return done
	}
	resultCh, ok := c.startFontBundleFlight(detachedRelayDemux(context.Background()), key, extract)
	if !ok {
		<-c.warmSem
		close(done)
		return done
	}
	// Wait for the shared flight even when this warm joined one led by another
	// caller: the caller's relay registration must outlive the extraction that
	// may be using it.
	go func() {
		defer close(done)
		defer func() { <-c.warmSem }()
		start := time.Now()
		elapsed := func() int64 { return time.Since(start).Milliseconds() }
		res := <-resultCh
		switch {
		case errors.Is(res.Err, errSubtitleFontBundleFlightSkipped):
			// Expected backpressure, not a source failure: the next trigger
			// retries once the relay's font budget frees up.
			slog.Debug("font bundle warm skipped: relay demux busy",
				"file_id", key.FileID, "elapsed_ms", elapsed())
		case errors.Is(res.Err, context.DeadlineExceeded), errors.Is(res.Err, context.Canceled):
			// The production failure this reporting exists for: every cold
			// dump hit its deadline and stored nothing, which the old
			// "finished" line hid while the client kept polling.
			slog.Warn("font bundle warm produced no bundle",
				"file_id", key.FileID, "elapsed_ms", elapsed(),
				"reason", "extraction deadline exceeded or was canceled")
		case res.Err != nil:
			slog.Warn("font bundle warm failed",
				"file_id", key.FileID, "elapsed_ms", elapsed(), "error", res.Err)
		default:
			data, _ := res.Val.([]byte)
			items := fontBundleItemCount(data)
			slog.Info("font bundle warm finished",
				"file_id", key.FileID, "elapsed_ms", elapsed(),
				"items", items, "bytes", len(data))
		}
	}()
	return done
}

// fontBundleEntryPath resolves the committed cache-entry path for the key and
// stats it, bumping its mtime for LRU eviction. ok is false when the cache is
// disabled, the key is not reliably keyable, or no entry exists.
func (c *SubtitleCache) fontBundleEntryPath(key FontBundleKey, now time.Time) (string, bool) {
	dir := c.dir()
	if dir == "" {
		return "", false
	}
	identity, modTime, size, ok := key.source(now)
	if !ok {
		return "", false
	}
	path := filepath.Join(dir, fontBundleCacheFileName(identity, modTime, size))
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	// Recency bump for LRU eviction.
	now = time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		slog.Debug("subtitle cache recency bump failed", "path", path, "error", err)
	}
	return path, true
}

// storeFontBundle publishes the encoded font-bundle bytes with an atomic
// temp-file + rename commit, following the payload cache's Commit pattern. The
// temp file carries the ".part-" marker so the stale-sibling sweep reclaims it
// after a crash.
func (c *SubtitleCache) storeFontBundle(key FontBundleKey, data []byte) error {
	dir := c.dir()
	if dir == "" {
		return nil
	}
	identity, modTime, size, ok := key.source(time.Now())
	if !ok {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	final := filepath.Join(dir, fontBundleCacheFileName(identity, modTime, size))
	tmp, err := os.CreateTemp(dir, filepath.Base(final)+".part-*")
	if err != nil {
		return fmt.Errorf("subtitle font bundle temp create: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("subtitle font bundle publish: %w", err)
	}
	// Evict stale font-bundle entries so they don't accumulate past the budget.
	// The recognizer in evict already handles .fontbundle files.
	c.evict(dir)
	return nil
}

func (c *SubtitleCache) release(fill *SubtitleCacheFill) {
	c.mu.Lock()
	if c.inflight[fill.key] == fill {
		close(fill.done)
		delete(c.inflight, fill.key)
	}
	c.mu.Unlock()
}

// join adds a viewer to a progressive fill that is still running. It
// reports false when the fill cannot be watched.
func (f *SubtitleCacheFill) join() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.progressive || f.ended || f.abandoned {
		return false
	}
	f.viewers++
	return true
}

// leave removes a viewer. The last one to leave a running fill orphans it.
func (f *SubtitleCacheFill) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.viewers--
	if f.viewers == 0 {
		if f.ended {
			f.closeAndRemoveTmp()
		} else if f.orphaned != nil {
			orphaned := f.orphaned
			f.orphaned = nil
			f.abandoned = !orphaned()
		}
	}
}

// watch writes the fill's output to w as the extract produces it, reading
// the temp file, so a slow or stalled client holds up only itself. It
// returns the bytes written once the fill ends or its temp file fails, or
// with the error that ended ctx or a write.
func (f *SubtitleCacheFill) watch(ctx context.Context, w io.Writer) (int64, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	var sent int64
	for {
		f.mu.Lock()
		if sent == f.written {
			if f.ended || f.failed {
				f.mu.Unlock()
				return sent, nil
			}
			grew := f.grew
			f.mu.Unlock()
			select {
			case <-grew:
				continue
			case <-ctx.Done():
				return sent, ctx.Err()
			}
		}
		toRead := min(int64(len(buf)), f.written-sent)
		n, _ := f.tmp.ReadAt(buf[:toRead], sent)
		f.mu.Unlock()
		if n == 0 {
			// An unreadable temp file: the caller extracts the rest itself.
			return sent, nil
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return sent, err
		}
		sent += int64(n)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// fail discards the fill, recording err for the requests watching it.
func (f *SubtitleCacheFill) fail(err error) {
	f.mu.Lock()
	f.err = err
	f.endLocked()
	hasViewers := f.viewers > 0
	f.mu.Unlock()
	if !hasViewers {
		f.closeAndRemoveTmp()
	}
	f.c.release(f)
}

func (f *SubtitleCacheFill) extractError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// endLocked marks the fill ended and wakes its watchers.
func (f *SubtitleCacheFill) endLocked() {
	if !f.ended {
		f.ended = true
		close(f.grew)
	}
}

// streamFullTrack streams a complete track that was not cached when the
// request arrived. The first request starts a fill and every request that
// arrives while it runs watches it, so the track is demuxed once however many
// requests ask for it (a player opening a sidecar can send several). When
// the fill commits, a watcher finishes from the cache entry. When the fill
// fails, the watcher reports its error. When the fill cannot serve the rest
// (its temp file failed, or its entry could not be published), the watcher
// runs its own extract and skips the bytes it already sent; an extract of the
// same source writes the same bytes. A request with no usable fill (caching off,
// or a text batch that writes its file only at the end) extracts for itself.
func (c *SubtitleCache) streamFullTrack(ctx context.Context, w io.Writer, opts StreamExtractOpts, format string, extract SUPExtractFunc) error {
	fill, inflight := c.reserveFill(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format, true)
	switch {
	case fill != nil:
		// A fill that committed after the caller's lookup already holds the track.
		if served, err := c.copyCachedFrom(w, opts, format, 0); served {
			fill.Discard()
			return err
		}
		fill.join()
		c.startFill(ctx, opts, extract, fill)
	case inflight != nil && inflight.join():
		fill = inflight
	case inflight != nil:
		if err := waitForSubtitleFill(ctx, inflight.done, c.fillWaitBound()); err != nil {
			if errors.Is(err, errSubtitleFillWaitTimeout) {
				return &SubtitleExtractionRetryError{RetryAfter: subtitleFillBusyRetryAfter}
			}
			return err
		}
		if served, err := c.copyCachedFrom(w, opts, format, 0); served {
			return err
		}
		// The leader may have failed without publishing an entry. Re-enter
		// reservation so this waiter takes ownership and its bytes are cached;
		// a direct uncached extract here would lose the takeover fill.
		return c.streamFullTrack(ctx, w, opts, format, extract)
	default:
		opts.Writer = w
		return extract(ctx, opts)
	}
	sent, err := fill.watch(ctx, w)
	fill.leave()
	if err != nil {
		return err
	}
	if served, err := c.copyCachedFrom(w, opts, format, sent); served {
		return err
	}
	if err := fill.extractError(); err != nil {
		return err
	}
	opts.Writer = &skipWriter{w: w, n: sent}
	return extract(ctx, opts)
}

// startFill runs extract into fill in the background. The extract writes
// only to the fill's temp file, which its viewers read. When the last viewer
// leaves before the end (a start that gave up, a replan or seek that reloads
// the item) the extract carries on in a warm slot, since the same track is
// usually asked for again seconds later and the demux behind it can take
// minutes on a large remux; with every slot busy it stops and the fill is
// discarded.
func (c *SubtitleCache) startFill(ctx context.Context, opts StreamExtractOpts, extract SUPExtractFunc, fill *SubtitleCacheFill) {
	extractCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subtitleCacheWarmTimeout)
	// holdsSlot is guarded by fill.mu, under which orphaned runs.
	var holdsSlot bool
	fill.mu.Lock()
	fill.orphaned = func() bool {
		select {
		case c.warmSem <- struct{}{}:
			holdsSlot = true
			return true
		default:
			cancel()
			return false
		}
	}
	fill.mu.Unlock()
	opts.Writer = fill.Tee(io.Discard)
	go func() {
		defer cancel()
		start := time.Now()
		err := extract(extractCtx, opts)
		// With the extract over, a viewer leaving has nothing to keep running.
		fill.mu.Lock()
		fill.orphaned = nil
		viewersLeft := holdsSlot
		fill.mu.Unlock()
		if viewersLeft {
			<-c.warmSem
		}
		if err != nil {
			fill.fail(err)
		} else if err := fill.Commit(); err != nil {
			slog.WarnContext(ctx, "subtitle cache commit failed",
				"input", opts.InputPath, "track", opts.TrackIndex, "error", err)
		} else if viewersLeft {
			slog.InfoContext(ctx, "subtitle cache fill finished after its viewers left",
				"input", opts.InputPath, "track", opts.TrackIndex,
				"elapsed_ms", time.Since(start).Milliseconds())
		}
	}()
}

// copyCachedFrom writes the committed entry for opts' track from byte off.
// It reports false when there is no entry at least off bytes long.
func (c *SubtitleCache) copyCachedFrom(w io.Writer, opts StreamExtractOpts, format string, off int64) (bool, error) {
	f, _, ok := c.lookup(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, format)
	if !ok {
		return false, nil
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || info.Size() < off {
		return false, nil
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return false, nil
	}
	return true, copyAndFlush(w, f)
}

// skipWriter drops the first n bytes written to it: a viewer that falls back
// to its own extract already received them from a fill.
type skipWriter struct {
	w io.Writer
	n int64
}

func (s *skipWriter) Write(p []byte) (int, error) {
	if s.n >= int64(len(p)) {
		s.n -= int64(len(p))
		return len(p), nil
	}
	skipped := int(s.n)
	s.n = 0
	n, err := s.w.Write(p[skipped:])
	return skipped + n, err
}

func (s *skipWriter) Flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
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
	t.fill.record(p)
	return t.w.Write(p)
}

func (t *subtitleTeeWriter) Flush() {
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

// record appends p to the temp file and wakes the fill's watchers.
func (f *SubtitleCacheFill) record(p []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.ended {
		return
	}
	if _, err := f.tmp.Write(p); err != nil {
		f.failed = true
		slog.Warn("subtitle cache tee write failed; continuing uncached",
			"track", f.trackIndex, "error", err)
	} else {
		f.written += int64(len(p))
		// Index the bytes that are now in the artifact, in the order they were
		// written, so the coverage watermark never describes bytes that are not
		// there.
		if f.sup != nil {
			f.sup.feed(p)
		}
	}
	close(f.grew)
	f.grew = make(chan struct{})
}

// Commit publishes the temp file as the cache entry: fsync, atomic rename,
// stale-sibling cleanup, then size-cap eviction. It refuses to publish (and
// discards instead) when a tee write failed or when the source file changed
// while the extract ran — a partial or mismatched entry must never be served.
func (f *SubtitleCacheFill) Commit() error {
	f.mu.Lock()
	failed := f.failed
	f.mu.Unlock()
	if failed {
		f.Discard()
		return errors.New("subtitle cache fill had write errors; discarded")
	}
	if identity, modTime, size, ok := subtitleCacheSource(f.inputPath, f.cacheIdentity, time.Now()); !ok ||
		identity != f.identity || !modTime.Equal(f.srcMtime) || size != f.srcSize {
		f.fail(errSubtitleSourceChanged)
		return errSubtitleSourceChanged
	}
	defer f.c.release(f)

	f.mu.Lock()
	err := f.publishLocked()
	f.endLocked()
	f.mu.Unlock()
	if err != nil {
		return err
	}

	dir := filepath.Dir(f.tmp.Name())
	f.c.removeStaleSiblings(dir, f.identity, f.trackIndex, f.key)
	f.c.evict(dir)
	return nil
}

func (f *SubtitleCacheFill) publishLocked() error {
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
	return nil
}

// Discard abandons the fill: the temp file is removed and the in-flight slot
// released. Safe to call after a failed Commit (idempotent enough — the temp
// file is already gone and re-removal is a no-op).
func (f *SubtitleCacheFill) Discard() {
	f.mu.Lock()
	f.closeAndRemoveTmp()
	f.endLocked()
	f.mu.Unlock()
	f.c.release(f)
}

func (f *SubtitleCacheFill) closeAndRemoveTmp() {
	_ = f.tmp.Close()
	if err := os.Remove(f.tmp.Name()); err != nil && !os.IsNotExist(err) {
		slog.Warn("subtitle cache temp remove failed", "path", f.tmp.Name(), "error", err)
	}
}
