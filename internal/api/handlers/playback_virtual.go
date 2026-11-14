package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	langpkg "github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/language"
)

const virtualPlaybackPrefix = "virtual://"

const maxVirtualPlaybackStreams = 50

// virtualCandidateSinkBudget bounds one detached first-playback candidate
// persist. Persisting the listing is best-effort metadata work behind the first
// bytes, so it carries its own budget rather than inheriting the cold-path
// deadline it was detached from.
var virtualCandidateSinkBudget = 15 * time.Second

// virtualCandidatePublishBudget bounds the versions_updated announce that
// follows a successful first-playback candidate persist. The persist and the
// publish are separate pieces of work: a slow-but-successful persist can spend
// the whole sink budget, so the publish gets a fresh short context derived from
// service shutdown rather than from the persist's context or the request.
// Without that, a persist near its deadline would leave no time for the Redis
// fan-out. It is a var so tests can shrink it, matching the other detached
// budgets.
var virtualCandidatePublishBudget = 5 * time.Second

const (
	defaultMaxVirtualFailoverAttempts = 5
	virtualProbeBudget                = 15 * time.Second
	// virtualStartProbeBudget caps the synchronous start-path probe. The start
	// response must not wait fifteen seconds for a remote byte source: past this
	// bound the plan serves the candidate's declared metadata and the deferred
	// probe owns verification. Background probes keep virtualProbeBudget.
	virtualStartProbeBudget         = 3 * time.Second
	maxVirtualPlaybackPrefetchFiles = 2
	virtualPlaybackPrefetchBudget   = 20 * time.Second
	// virtualPrefetchQueueSize bounds pending prefetch work: how many distinct
	// source/profile prefetches may wait for a worker. virtualPrefetchWorkers
	// bounds active work. Together they bound the dedup map, whose entries exist
	// only while a task is pending or active.
	virtualPrefetchQueueSize = 64
	virtualPrefetchWorkers   = 2
	// virtualProbeFailureTTL is the base damper window and
	// virtualProbeFailureMaxTTL caps its exponential growth. A candidate that
	// just consumed the whole virtualProbeBudget without producing usable
	// metadata is not probed again for the current window; the resolver falls
	// through to the candidate-declared metadata instead. Each repeated failure
	// for the same key doubles the window (5m → 10m → 20m → 40m → 60m), so a
	// permanently unprobeable source is retried at most about once per hour. A
	// successful probe clears the marker entirely: this is a damper, not a
	// cache.
	virtualProbeFailureTTL    = 5 * time.Minute
	virtualProbeFailureMaxTTL = 60 * time.Minute
	// virtualProbeSizeMismatchTTL is the wrong-source backoff. A probe that
	// finds a different release than the row declares is not a dead candidate:
	// the provider cache is serving the wrong bytes and may heal within
	// minutes. The marker lives under its own key (see
	// virtualProbeSizeMismatchKey) and uses this short fixed window instead of
	// the dead-candidate exponential backoff, so the row is re-probed promptly
	// once the provider serves the release again.
	virtualProbeSizeMismatchTTL = time.Minute
	// virtualProbeFailureRepeatThreshold is the number of consecutive failures
	// for one candidate before an unpin is allowed. A single transient failure
	// (outer budget fired, provider RPC timeout) leaves the candidate's health
	// unknown; only a repeat is treated as evidence it should stop steering
	// starts.
	virtualProbeFailureRepeatThreshold = 2
	// virtualProbePartialRepeatThreshold is the number of consecutive
	// incomplete enumerations for one candidate before the non-negotiated
	// paths stop re-probing and settle on the candidate's declared metadata.
	// The negotiated path arms a convergence watchdog that fails the pending
	// lifecycle at its own bound; the non-negotiated background path and the
	// synchronous resolve have no session lifecycle to fail, so this streak is
	// their bound. A single partial read is never penalized (the pin is kept
	// and the failure damper is not consumed); only a persistent streak settles
	// the resolve so a truncated provider cannot keep a start pending for the
	// session's life.
	virtualProbePartialRepeatThreshold = 2
	// virtualProbeFailureMaxEntries caps the process-wide probe-failure damper.
	// A live marker is a candidate still inside its backoff window; a
	// long-lived server that probes many distinct candidates would otherwise
	// retain one marker per candidate forever. The live set is bounded in
	// practice by how many distinct candidates can fail inside the 60m maximum
	// window, so 4096 is far above any plausible concurrent failure set and
	// eviction only fires under pathological churn. It does not change when a
	// retained marker counts as valid: an evicted live marker simply means the
	// next probe for that candidate runs sooner than its backoff would have
	// allowed.
	virtualProbeFailureMaxEntries = 4096
)

// virtualStartupBudget bounds the entire cold path: candidate listing,
// provider resolution, probing, retries and the stale-source
// fallback all run under one deadline started at cold-path entry, so a slow
// early stage leaves a later stage only the remaining budget. It is a var so
// tests can shrink the budget and observe the single deadline.
var virtualStartupBudget = 60 * time.Second

// virtualBackgroundProbeBudget bounds a background probe. Production uses the
// scanner's shared probe timeout so the inner probe and the caller that waits
// for it cannot drift; it is a var so tests can shrink the wait.
var virtualBackgroundProbeBudget = scanner.VirtualProbeTimeout

// virtualProbeFailureMark records the most recent failure for one candidate,
// the backoff window derived from the number of consecutive failures, and the
// running failure count that gates unpinning.
//
// A wrong-source marker additionally carries the observed byte verdict: the
// bytes the probe read (servedBytes) and the bytes the catalog row declares
// (declaredBytes). That lets the version list report the mismatch as a
// distinct state instead of inferring it from a missing probe. Both stay zero
// on every other marker kind.
type virtualProbeFailureMark struct {
	ttl           time.Duration
	expiresAt     time.Time
	failures      int
	servedBytes   int64
	declaredBytes int64
}

// virtualProbeFailureCache remembers the last failed probe per candidate so a
// replan does not pay the probe budget again. It is package-level because the
// handler is shared across requests and the marker is advisory: a mutex keeps
// concurrent starts safe, and the small map is bounded by the live candidate
// set (entries that have been idle past the maximum backoff are dropped).
// now is injectable for tests and defaults to time.Now.
type virtualProbeFailureCache struct {
	mu    sync.Mutex
	marks map[string]virtualProbeFailureMark
	now   func() time.Time
}

// virtualCollectionProbeSource marks a collection-sourced virtual row whose
// pin was recorded by the collection variant path rather than a per-file listing.
const virtualCollectionProbeSource = "virtual_collection"

func (c *virtualProbeFailureCache) clock() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *virtualProbeFailureCache) recent(key string) bool {
	if c == nil || key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	mark, ok := c.marks[key]
	if !ok {
		return false
	}
	now := c.clock()
	if !now.Before(mark.expiresAt) {
		delete(c.marks, key)
		return false
	}
	// At the cap, drop markers whose window has lapsed. They are already
	// ignored by recent/count, so this changes nothing about validity; it
	// keeps the map from holding memory the damper will never consult.
	if len(c.marks) >= virtualProbeFailureMaxEntries {
		c.pruneExpiredLocked(now)
	}
	return true
}

// pruneExpiredLocked drops every marker whose backoff window has lapsed. Caller
// holds c.mu.
func (c *virtualProbeFailureCache) pruneExpiredLocked(now time.Time) {
	for k, mark := range c.marks {
		if !now.Before(mark.expiresAt) {
			delete(c.marks, k)
		}
	}
}

// sweepLocked makes room when the map is at its cap. It first drops lapsed
// markers (behaviorally dead), then, if every remaining marker is still live,
// evicts the one closest to lapsing. The soonest-expiring marker is the least
// useful: it would stop suppressing within the shortest time anyway. Caller
// holds c.mu.
func (c *virtualProbeFailureCache) sweepLocked(now time.Time) {
	if len(c.marks) < virtualProbeFailureMaxEntries {
		return
	}
	c.pruneExpiredLocked(now)
	if len(c.marks) < virtualProbeFailureMaxEntries {
		return
	}
	victim := ""
	var victimAt time.Time
	for k, mark := range c.marks {
		if victim == "" || mark.expiresAt.Before(victimAt) || (mark.expiresAt.Equal(victimAt) && k < victim) {
			victim = k
			victimAt = mark.expiresAt
		}
	}
	if victim != "" {
		delete(c.marks, victim)
	}
}

func (c *virtualProbeFailureCache) mark(key string) {
	c.markFor(key, virtualProbeFailureTTL, virtualProbeFailureMaxTTL)
}

// markFor records a failure for key with base as the first backoff window and
// maxTTL as the cap, doubling the window on every repeat. Callers that need a
// different backoff posture (the wrong-source damper) use this directly
// instead of mark.
func (c *virtualProbeFailureCache) markFor(key string, baseTTL, maxTTL time.Duration) {
	c.markForBytes(key, baseTTL, maxTTL, 0, 0)
}

// markForBytes is markFor with the observed served/declared byte verdict
// attached. Only the wrong-source marker passes non-zero sizes; every other
// mark leaves them zero so the version list never reads a size verdict from a
// dead-candidate or partial-enumeration marker.
func (c *virtualProbeFailureCache) markForBytes(key string, baseTTL, maxTTL time.Duration, servedBytes, declaredBytes int64) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.marks == nil {
		c.marks = make(map[string]virtualProbeFailureMark)
	}
	now := c.clock()
	// Make room before inserting a new marker. An update of an existing key
	// does not grow the map, so it never needs the sweep and never evicts the
	// marker it is about to refresh.
	if _, exists := c.marks[key]; !exists && len(c.marks) >= virtualProbeFailureMaxEntries {
		c.sweepLocked(now)
	}
	// Prune markers that have been idle well past their window so a long-lived
	// process only retains failures still in backoff.
	for k, mark := range c.marks {
		if now.After(mark.expiresAt.Add(virtualProbeFailureMaxTTL)) {
			delete(c.marks, k)
		}
	}
	ttl := baseTTL
	failures := 1
	if prev, ok := c.marks[key]; ok {
		failures = prev.failures + 1
		ttl = prev.ttl * 2
		if ttl > maxTTL {
			ttl = maxTTL
		}
	}
	c.marks[key] = virtualProbeFailureMark{
		ttl: ttl, expiresAt: now.Add(ttl), failures: failures,
		servedBytes: servedBytes, declaredBytes: declaredBytes,
	}
}

// count returns the number of consecutive failures recorded for key, or 0 when
// no live marker exists. An expired marker is dropped on read, matching recent.
func (c *virtualProbeFailureCache) count(key string) int {
	if c == nil || key == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	mark, ok := c.marks[key]
	if !ok {
		return 0
	}
	now := c.clock()
	if !now.Before(mark.expiresAt) {
		delete(c.marks, key)
		return 0
	}
	if len(c.marks) >= virtualProbeFailureMaxEntries {
		c.pruneExpiredLocked(now)
	}
	return mark.failures
}

// virtualProbePartialKey namespaces the incomplete-enumeration streak for a
// candidate. It is separate from the candidate's failure key on purpose: a
// partial read must never arm the probe-failure damper (recent/count) by
// itself, because it is not a verdict about the candidate's bytes. Keeping the
// streak under its own key lets the counter bound a persistent streak without
// suppressing the probe the failure damper gates.
func virtualProbePartialKey(key string) string {
	if key == "" {
		return ""
	}
	return key + "\x00incomplete"
}

// bumpPartial records one more incomplete enumeration for a candidate and
// returns the new consecutive streak length. It is the bound side of the
// non-negotiated paths (the background probe and the synchronous resolve),
// which have no convergence watchdog to fail a stuck pending lifecycle.
func (c *virtualProbeFailureCache) bumpPartial(key string) int {
	partialKey := virtualProbePartialKey(key)
	if partialKey == "" {
		return 0
	}
	c.mark(partialKey)
	return c.count(partialKey)
}

// clearPartial drops a candidate's incomplete-enumeration streak. It is called
// once a complete enumeration lands so a later partial starts a fresh streak.
func (c *virtualProbeFailureCache) clearPartial(key string) {
	c.clear(virtualProbePartialKey(key))
}

// virtualProbeSizeMismatchKey namespaces the wrong-source damper for a
// candidate. It is separate from the candidate's failure key on purpose: a
// probe that read a different release's bytes says the provider cache is stale,
// not that the release is dead, so it must never feed the dead-candidate
// backoff or its repeat threshold.
func virtualProbeSizeMismatchKey(key string) string {
	if key == "" {
		return ""
	}
	return key + "\x00size-mismatch"
}

// markSizeMismatch arms the short wrong-source damper for a candidate and
// records the observed byte verdict. It uses one fixed window
// (virtualProbeSizeMismatchTTL) rather than the dead-candidate exponential
// backoff, so the row is re-probed promptly once the provider serves the release
// again. servedBytes is what the probe actually read; declaredBytes is the size
// the catalog row advertises. The pair is what the version list reports as a
// source mismatch while the marker lives.
func (c *virtualProbeFailureCache) markSizeMismatch(key string, servedBytes, declaredBytes int64) {
	c.markForBytes(virtualProbeSizeMismatchKey(key), virtualProbeSizeMismatchTTL, virtualProbeSizeMismatchTTL, servedBytes, declaredBytes)
}

// sizeMismatchVerdict returns the served-vs-declared byte verdict recorded by a
// live wrong-source marker for a candidate. The marker is the same short damper
// that backs the row off, so this reports a mismatch only while that recent
// probe verdict still applies: an accepted probe clears the marker, and an
// expired one stops reporting. The boolean is false when no live, size-bearing
// marker exists.
func (c *virtualProbeFailureCache) sizeMismatchVerdict(key string) (servedBytes, declaredBytes int64, ok bool) {
	if c == nil || key == "" {
		return 0, 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	markerKey := virtualProbeSizeMismatchKey(key)
	mark, found := c.marks[markerKey]
	if !found {
		return 0, 0, false
	}
	if !c.clock().Before(mark.expiresAt) {
		delete(c.marks, markerKey)
		return 0, 0, false
	}
	if mark.servedBytes <= 0 || mark.declaredBytes <= 0 {
		return 0, 0, false
	}
	return mark.servedBytes, mark.declaredBytes, true
}

// clearSizeMismatch drops a candidate's wrong-source marker after a probe
// accepted the bytes it served.
func (c *virtualProbeFailureCache) clearSizeMismatch(key string) {
	c.clear(virtualProbeSizeMismatchKey(key))
}

// virtualProbeBackedOff reports whether either probe damper currently suppresses
// a new probe for key: the dead-candidate failure damper or the shorter
// wrong-file-served damper. Keep the two markers distinct so a stale provider
// cache cannot keep a live release out for the dead-source backoff.
func virtualProbeBackedOff(key string) bool {
	return virtualProbeFailures.recent(key) || virtualProbeFailures.recent(virtualProbeSizeMismatchKey(key))
}

// virtualProbeVerdictUnknown reports whether an error leaves the candidate's
// health unknown rather than condemning it: the caller's outer budget fired or
// the request was canceled while the probe was still running under its own
// timeout, or the probe returned only a partial enumeration. The probe may yet
// complete in the cache, so this must not unpin.
func virtualProbeVerdictUnknown(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, scanner.ErrVirtualProbePartial)
}

func (c *virtualProbeFailureCache) clear(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.marks, key)
}

// virtualProbeFailures is the process-wide probe failure damper. Tests may
// clear entries directly.
var virtualProbeFailures = &virtualProbeFailureCache{marks: make(map[string]virtualProbeFailureMark)}

// virtualFailedVerdictMaxAge is the backstop age after which a durable
// media_files.failed_at verdict stops excluding a candidate from automatic
// selection. The primary lifecycle is explicit: a verdict is cleared by a real
// delivery (MarkVirtualCandidateRecovered), by an explicit user retry
// (allowFailedCandidate), or by a fresh candidate listing replacing the row.
// This age exists only so a row that is never listed again cannot stay
// excluded forever; it is deliberately long so a genuinely dead provider URL
// is not retried on every start. It is a var so tests can shrink it; the
// production value is catalog.FailedVerdictMaxAge so every surface that
// excludes by failed_at applies the same window.
var virtualFailedVerdictMaxAge = catalog.FailedVerdictMaxAge

// ErrVirtualCandidateMarkedFailed reports that a virtual candidate cannot be
// selected because its catalog row carries an active failed_at verdict.
var ErrVirtualCandidateMarkedFailed = errors.New("candidate is marked failed")

// errVirtualRotationExhaustedV3 marks a replan rehydration that already
// declared candidate rotation and still found no viable provider candidate.
// The rotation retry is the authoritative signal: it re-lists and substitutes
// within the release, so its failure means the release has nothing left to
// serve. A caller that would otherwise walk sibling editions can stop without
// re-paying the resolve and probe budgets: every virtual sibling resolves
// through the same provider, and the provider has just answered that it has
// nothing for this release.
var errVirtualRotationExhaustedV3 = errors.New("virtual replan candidate rotation exhausted")

// isRehydratedVirtualSourceRotatableV3 reports whether an error from
// resolving a session-bound candidate indicates that the candidate cannot
// be served (absent from the provider's current list, or marked failed),
// justifying a rotation to a live sibling during replan rehydration.
func isRehydratedVirtualSourceRotatableV3(err error) bool {
	return errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) ||
		errors.Is(err, ErrVirtualCandidateMarkedFailed)
}

// virtualCandidateVerdictActive reports whether a failed_at stamp still
// excludes a candidate from automatic resolution and adoption. An explicit
// retry (allowFailedCandidate) is handled by callers and bypasses this check;
// this helper only answers the passive-eligibility question.
func virtualCandidateVerdictActive(failedAt *time.Time, now time.Time) bool {
	return catalog.FailedVerdictActive(failedAt, now, virtualFailedVerdictMaxAge)
}

// virtualP0ProbeStalenessBound is how old a pinned row's probe evidence may be
// before the P0 repeat-play fast path revalidates against the provider instead
// of serving the pin blind. It is a fixed multiple of the background candidate
// refresh cadence (virtualCandidateRefreshInterval): 24 ticks, twelve hours,
// spans a normal viewing day, so a same-day replay keeps the fast path while a
// provider re-list that dropped the pinned release is re-checked within the
// same day. It is deliberately a fixed constant rather than the candidate store
// window: that window defaults to 720h, which would leave a vanished pin
// effectively unvalidated for a month. No new setting is introduced.
const virtualP0ProbeStalenessBound = 24 * virtualCandidateRefreshInterval

// p0PinSuspectReason classifies why the P0 repeat-play fast path must not bind
// the pinned row blind. An empty result means the row is fresh and eligible.
// A nil row is always suspect (there is nothing to bind).
func p0PinSuspectReason(file *models.MediaFile, now time.Time) string {
	if file == nil {
		return "missing_row"
	}
	if virtualCandidateVerdictActive(file.FailedAt, now) {
		return "failed_verdict"
	}
	if file.ProbeUpdatedAt != nil && now.Sub(*file.ProbeUpdatedAt) > virtualP0ProbeStalenessBound {
		return "stale_probe"
	}
	return ""
}

// p0PinSuspect reports whether the pinned row needs a provider revalidation
// before the P0 fast path may serve it. See p0PinSuspectReason.
func p0PinSuspect(file *models.MediaFile, now time.Time) bool {
	return p0PinSuspectReason(file, now) != ""
}

// logVirtualP0SuspectSkippedV3 records that a row which otherwise qualified for
// the P0 repeat-play fast path was revalidated instead, because its verdict was
// active or its probe evidence was stale. Distinct from the incomplete-metadata
// skip so deployments can see why a replay cost a provider round-trip.
func logVirtualP0SuspectSkippedV3(ctx context.Context, file *models.MediaFile, candidateURI, reason string) {
	if file == nil {
		return
	}
	slog.InfoContext(ctx, "virtual repeat-play fast path skipped: pinned row needs revalidation",
		"component", "api",
		"status", "fast_path_skipped_suspect_pin",
		"reason", reason,
		"file_id", file.ID,
		"content_id", file.ContentID,
		"candidate_uri", candidateURI,
	)
}

// clearVirtualCandidateVerdict clears the row's stale failed_at after a
// successful same-identity re-resolve. The write is fenced on the row identity
// and the verdict observed before the resolve, so a concurrent rotation or a
// newer failure is never cleared. Best-effort: a clear failure does not fail
// the playback that already resolved, it only leaves the row slow next replay.
func (h *PlaybackHandler) clearVirtualCandidateVerdict(ctx context.Context, file *models.MediaFile, resolvedURI string) {
	if h == nil || h.VirtualCandidateClearFailedMarker == nil || file == nil || file.FailedAt == nil {
		return
	}
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := h.VirtualCandidateClearFailedMarker(clearCtx, file.ID, file.FilePath, file.FailedAt); err != nil {
		slog.WarnContext(ctx, "virtual candidate verdict clear failed",
			"component", "api", "file_id", file.ID, "candidate_uri", resolvedURI, "error", err)
		return
	}
	slog.InfoContext(ctx, "virtual candidate verdict cleared after a successful same-identity re-resolve",
		"component", "api", "status", "verdict_cleared", "file_id", file.ID, "candidate_uri", resolvedURI)
}

// startCandidateFailStampBudget bounds one detached failed_at stamp write. The
// stamp is fired off the resolve path, so it carries its own budget instead of
// inheriting the walk's decision deadline; a var so tests can shrink it.
var startCandidateFailStampBudget = 3 * time.Second

// virtualCandidateConfirmedDead classifies a resolve failure as a confirmed-dead
// pin for the start path: the provider answered and the pinned release is gone
// or unusable. It is a pure predicate so callers can decide whether a stamp is
// warranted without performing the write, which lets the write be detached from
// the resolve that produced the verdict.
//
// Only the code's own dead verdict counts (isVirtualCandidateDeadError) plus an
// absent pin that carries durable identity; an empty provider listing is a
// transient hiccup and is deliberately never dead (the versions check documents
// why: a 2.6s empty-listing burst once marked 50 of 57 rows dead). Any
// transport-temporary cause in the chain short-circuits the whole verdict before
// the absent-pin branch is consulted, so a joined outage+absent shape cannot
// smuggle a durable indictment past the transient guard. This mirrors the
// pre-verdict guard in checkVersion (catalog_versions_check.go).
func virtualCandidateConfirmedDead(file *models.MediaFile, resolveErr error) bool {
	if file == nil || resolveErr == nil {
		return false
	}
	// Transport-temporary shapes (provider outage, deadline, pending release,
	// empty listing) are availability-shaped and say nothing about the pinned
	// release, so they must be classified BEFORE either dead-verdict branch. A
	// joined error can carry both a transient listing cause and an absent-pin
	// sentinel (for example errors.Join(resolver.ErrProviderUnavailable,
	// virtuallibrary.ErrSessionBoundCandidateAbsent)); letting the identity
	// branch read the absent sentinel as a verdict is what let a provider flap
	// durably indict a pin.
	if isVirtualProviderListingTemporaryError(resolveErr) {
		return false
	}
	if isVirtualCandidateDeadError(resolveErr) {
		return true
	}
	if _, hasIdentity := persistedVirtualIdentity(file); hasIdentity &&
		errors.Is(resolveErr, virtuallibrary.ErrSessionBoundCandidateAbsent) {
		return true
	}
	return false
}

// markVirtualCandidateFailed performs one fenced failed_at stamp for a candidate
// the classifier already found confirmed-dead, on the caller's context. The
// caller owns the write's budget and detachment: the synchronous start path
// hands it a context.WithoutCancel with startCandidateFailStampBudget, while the
// version-fallback walk hands it a detached, budgeted context. Best-effort: a
// stamp failure does not change the resolve outcome the caller already has.
func (h *PlaybackHandler) markVirtualCandidateFailed(ctx context.Context, file *models.MediaFile) {
	if h == nil || h.VirtualCandidateFailMarker == nil || file == nil || file.FailedAt != nil {
		return
	}
	if err := h.VirtualCandidateFailMarker(ctx, file.ID, file.FilePath, file.FailedAt); err != nil {
		slog.WarnContext(ctx, "mark virtual playback candidate failed",
			"component", "api", "file_id", file.ID, "candidate_uri", file.FilePath, "error", err)
		return
	}
	slog.InfoContext(ctx, "virtual playback candidate indicted after a confirmed-dead resolve",
		"component", "api", "status", "candidate_failed", "file_id", file.ID, "candidate_uri", file.FilePath)
}

// stampStartVirtualCandidateFailed applies the start path's fenced failed_at
// verdict synchronously, so the caller can rely on the write having landed
// before it returns. It is used by the primary terminal path, which is not
// joined by a bounded walk. A caller that must not wait on the write (the
// parallel version-fallback walk) uses stampStartVirtualCandidateFailedAsync
// instead.
func (h *PlaybackHandler) stampStartVirtualCandidateFailed(ctx context.Context, file *models.MediaFile, resolveErr error) {
	if h == nil || h.VirtualCandidateFailMarker == nil || file == nil || file.FailedAt != nil {
		return
	}
	if !virtualCandidateConfirmedDead(file, resolveErr) {
		return
	}
	stampCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), startCandidateFailStampBudget)
	defer cancel()
	h.markVirtualCandidateFailed(stampCtx, file)
}

// stampStartVirtualCandidateFailedAsync classifies synchronously (a pure
// predicate) and persists the verdict on a detached, budgeted goroutine. It is
// the version-fallback walk's stamp: the walk joins its workers before
// returning a terminal, so a synchronous marker write near the decision
// deadline would stretch the walk by up to startCandidateFailStampBudget
// (three seconds) past that deadline. Detaching the write keeps the walk bounded
// to its decision budget while still recording a confirmed-dead verdict, and the
// detached context keeps request values for logging and follows the service
// lifecycle so shutdown stops an outstanding stamp.
//
// The detached write is admitted through the handler's bounded detached-work
// gate. Without that bound each confirmed-dead alternate would spawn an
// unrestricted goroutine, so a slow database would accumulate one stuck write
// per alternate across concurrent starts; startCandidateFailStampBudget bounds
// each write's lifetime but not how many are in flight. Admission is
// non-blocking and drop-on-exhaustion: the walk must never wait on a slot, so a
// shed stamp leaves the row unindicted and only costs a later start the same
// resolve, exactly like the other best-effort detached stamps.
func (h *PlaybackHandler) stampStartVirtualCandidateFailedAsync(ctx context.Context, file *models.MediaFile, resolveErr error) {
	if h == nil || h.VirtualCandidateFailMarker == nil || file == nil || file.FailedAt != nil {
		return
	}
	if !virtualCandidateConfirmedDead(file, resolveErr) {
		return
	}
	gate := h.detachedGate()
	if !gate.tryAcquire() {
		slog.DebugContext(ctx, "virtual candidate verdict stamp skipped: detached worker budget exhausted",
			"component", "api", "file_id", file.ID, "candidate_uri", file.FilePath)
		return
	}
	stampCtx, cancel := h.virtualDetachedContext(ctx, startCandidateFailStampBudget)
	go func() {
		defer gate.release()
		defer cancel()
		h.markVirtualCandidateFailed(stampCtx, file)
	}()
}

// virtualFallbackEligibility is the explicit release-identity contract for the
// stale-source fallback. The caller builds it from the resolve intent and the
// anchored release so the fallback never infers session binding or rotation
// from a defaulted context value. It is enforced both before resolving a
// sibling and before persisting a replacement.
type virtualFallbackEligibility struct {
	// sessionBound is true when an existing session is already serving the
	// anchored release (a replan rehydration or serve-layer re-resolve).
	sessionBound bool
	// rotationAllowed is true only when the caller explicitly declared
	// candidate rotation (a verdict that indicts the release). Session-bound
	// playback must not change releases without it.
	rotationAllowed bool
	// allowFailed permits re-resolving a candidate whose failed_at verdict is
	// still active: the explicit retry policy. It never by itself authorizes a
	// sibling release.
	allowFailed bool
	// releaseID is the concrete release identity (the ?result= candidate id)
	// the fallback is anchored to. It is carried for logging and for asserting
	// that a same-release refresh did not drift.
	releaseID string
	// excludedCandidateIDs are provider result ids a confirmed verdict has
	// indicted. The fallback must never serve or adopt one: without this the
	// re-list can hand back a release an earlier hop already proved bad, which
	// is exactly the A→B→C→A cycle the durable chain exists to stop.
	excludedCandidateIDs []string
	// anchorErr captures a resolution failure of the session-bound anchor so
	// callers can preserve the typed cause (e.g. ErrSessionBoundCandidateAbsent)
	// across the fallback return instead of losing it to nil.
	anchorErr *error
}

// candidateExcluded reports whether candidateID is in the fallback's exclusion
// set.
func (e virtualFallbackEligibility) candidateExcluded(candidateID string) bool {
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return false
	}
	for _, excluded := range e.excludedCandidateIDs {
		if strings.TrimSpace(excluded) == candidateID {
			return true
		}
	}
	return false
}

// allowsSibling reports whether the fallback may resolve (and, when it wins,
// persist) a release other than the anchored one. A session-bound request may
// only do so under explicit rotation.
func (e virtualFallbackEligibility) allowsSibling() bool {
	return !e.sessionBound || e.rotationAllowed
}

// virtualProbeFailureKey identifies a probe target across replans. The resolved
// stream URL carries rotating credentials, so the candidate's provider-neutral
// identity is the stable key. The candidate's own result= identity and the
// owner installation are part of the key: two candidates under one neutral
// path, or the same candidate owned by two installations, are independent probe
// targets and a failure for one must not damp the others.
func virtualProbeFailureKey(candidateURI string, ownerInstallationID int) string {
	candidateID := virtualResultCandidateID(candidateURI)
	if candidateID == "" {
		candidateID = candidateURI
	}
	return virtualPlaybackNeutralKey(candidateURI) + "\x00" + strconv.Itoa(ownerInstallationID) + "\x00" + candidateID
}

// classifyVirtualServedEvidenceV3 is the pin-aware probe evidence gate every
// acceptance point uses. It compares the served bytes against the pin's
// recorded observed size when one is still valid, and records the pin's first
// served-size observation when the acceptance leans on a measured duration that
// proves the content past a rounded provider declaration. Recording is not
// admission: that first serve stays unverified and only a later serve that
// matches the recorded size is admitted, so one plausible-looking wrong serve
// never becomes the pin's identity. The row's declared FileSize is never
// rewritten: it stays the release's advertised size for the AltMount-completion
// comparison and every other declared-size decision. A pin with no concrete
// ?result= candidate (and therefore no stable byte identity) keeps the plain
// declared-size comparison.
func classifyVirtualServedEvidenceV3(pin string, declaredBytes int64, declared, probed *models.MediaFile, expectedRuntimeMinutes int) scanner.VirtualServedEvidenceVerdict {
	return scanner.ClassifyVirtualServedEvidenceForProbe(
		pin, declaredBytes, declared, probed, expectedRuntimeMinutes,
	)
}

// virtualDeclaredProbeFile is the declaration a probe result is judged against
// at a seam that only carries the post-probe file. The row supplies the
// persisted declaration; the served candidate supplies the listing labels the
// row may not have (an unprobed neutral row carries no resolution or codec).
// Only empty fields are filled, so measured evidence is never overwritten.
func virtualDeclaredProbeFile(file *models.MediaFile, cand VirtualPlaybackStream) *models.MediaFile {
	if file == nil {
		return nil
	}
	declared := *file
	if strings.TrimSpace(declared.Resolution) == "" {
		declared.Resolution = cand.Resolution
	}
	if strings.TrimSpace(declared.CodecVideo) == "" {
		declared.CodecVideo = cand.CodecVideo
	}
	return &declared
}

// virtualProbeEvidenceWithholdsIdentity reports whether this verdict must not
// be stamped as the row's release identity or made sticky. An unverified
// verdict is usable: its measured inventory (tracks and duration) is written so
// clients can report the real file and the plan can be built from it. It is not
// the pin's identity, though: a fresh probe stamp lets the next replay take the
// repeat-play fast path on that stamp alone, for the stamp's whole freshness
// window (virtualP0ProbeStalenessBound), without ever re-probing. A persisted
// inventory with a stamp would make one unconfirmed serve the pin's identity by
// persistence instead of by evidence. Such a write is therefore metadata-only
// (StampProbe false) and leaves the pin unpinned; the observation itself stays
// in memory, so the next serve is compared against it and admits the pin when it
// lands on the same bytes.
//
// This holds for an unpinned candidate too. It cannot become an observation
// (nothing is recorded without a concrete ?result= pick), but an unverified
// verdict there means the gap was beyond the plausibility ratio, so there is no
// identity to persist either.
//
// Mismatch never reaches a persist decision: every caller rotates or rejects
// first. An admitted verdict is written and pinned exactly as before.
func virtualProbeEvidenceWithholdsIdentity(verdict scanner.VirtualServedEvidenceVerdict) bool {
	return !verdict.EstablishesIdentity()
}

// virtualDetachedWorkerCap bounds detached virtual-playback work server-wide
// per handler. Every goroutine spawned per request or per candidate acquires
// one of these slots before it starts: background probes, optimistic
// revalidation, candidate-sink writes, subtitle searches, and prefetch. The
// value is deliberately small because one slot can hold a remote resolve or
// ffprobe for up to a minute. Acquisition is non-blocking, so the request path
// sheds best-effort work rather than waiting. Probe evidence persistence has
// its own bounded queue instead (see enqueueVirtualProbeEvidence) so a burst
// of long probes cannot crowd delivery/failure evidence out.
const virtualDetachedWorkerCap = 32

// virtualSubtitleSearchBudget bounds one detached subtitle search. The search
// used to run on context.Background() with no deadline, so a hung provider or
// downloader leaked the goroutine forever. On timeout or shutdown the in-flight
// dedupe key is released and a later start may retry.
const virtualSubtitleSearchBudget = 2 * time.Minute

// virtualDetachedGate is a non-blocking counting semaphore for detached work.
// A nil gate admits everything so handlers built as literals in tests behave as
// before; the handler constructs a bounded one lazily through detachedGate.
type virtualDetachedGate struct {
	slots chan struct{}
}

func newVirtualDetachedGate(capacity int) *virtualDetachedGate {
	if capacity <= 0 {
		capacity = virtualDetachedWorkerCap
	}
	return &virtualDetachedGate{slots: make(chan struct{}, capacity)}
}

func (g *virtualDetachedGate) capacity() int {
	if g == nil {
		return 0
	}
	return cap(g.slots)
}

// tryAcquire takes a slot without blocking. A false result means the caller
// must skip the best-effort work and return; it must not queue or wait, or a
// slow worker would stall the request path.
func (g *virtualDetachedGate) tryAcquire() bool {
	if g == nil {
		return true
	}
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// acquire takes a slot, blocking until one is free or ctx ends. It is only for
// dedicated background workers that are allowed to wait off the request path
// (the post-commit probe pool): the request path must keep using tryAcquire so
// it never stalls. A nil gate admits immediately.
func (g *virtualDetachedGate) acquire(ctx context.Context) bool {
	if g == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case g.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release returns a slot. It never blocks: a release without a matching acquire
// is a bug, and blocking on it would make things worse.
func (g *virtualDetachedGate) release() {
	if g == nil {
		return
	}
	select {
	case <-g.slots:
	default:
	}
}

// detachedGate returns the handler's bounded detached-work gate, constructing
// it lazily for handlers built outside NewPlaybackHandler (tests).
func (h *PlaybackHandler) detachedGate() *virtualDetachedGate {
	if h == nil {
		return nil
	}
	h.detachedWorkOnce.Do(func() {
		if h.detachedWorkGate == nil {
			h.detachedWorkGate = newVirtualDetachedGate(virtualDetachedWorkerCap)
		}
	})
	return h.detachedWorkGate
}

// virtualSubtitleSearchCap bounds concurrent detached subtitle searches on
// top of the aggregate detached gate. A search can hold a provider RPC and a
// download for the whole virtualSubtitleSearchBudget, so without a tighter cap
// a handful of stalled providers would consume the aggregate gate and starve
// probes and prefetch. Four searches in flight is far more than a household
// needs and still leaves the aggregate gate mostly free.
const virtualSubtitleSearchCap = 4

// subtitleSearchGate returns the handler's dedicated, bounded subtitle-search
// gate, constructing it lazily for handlers built as literals (tests).
func (h *PlaybackHandler) subtitleSearchGate() *virtualDetachedGate {
	if h == nil {
		return nil
	}
	h.subtitleSlotsOnce.Do(func() {
		if h.subtitleSlots == nil {
			h.subtitleSlots = newVirtualDetachedGate(virtualSubtitleSearchCap)
		}
	})
	return h.subtitleSlots
}

// virtualDetachedContext builds the context for one detached worker. It keeps
// base's values (so request-scoped logging and identity still flow) while
// discarding base's cancellation, and additionally cancels when the handler's
// service context ends so shutdown stops outstanding work. It always applies
// its own timeout. A nil base uses the service context directly.
func (h *PlaybackHandler) virtualDetachedContext(base context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	service := context.Background()
	if h != nil && h.ServiceContext != nil {
		service = h.ServiceContext
	}
	if base == nil {
		return context.WithTimeout(service, timeout)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(base), timeout)
	stop := context.AfterFunc(service, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// virtualPrefetchTask is one admitted, deduplicated unit of prefetch work. The
// file is copied at admission so a caller that mutates its slice afterward
// cannot change what the worker prefetches, and it carries the exact identity
// the admission key was computed from.
type virtualPrefetchTask struct {
	file       models.MediaFile
	neutralURI string
	userID     int
	profileID  string
	key        string
	// generation is the write generation captured at admission. The detached
	// prefetch uses it for its cache warm so a prefetch that started before a
	// foreground start cannot overwrite the newer listing when it finishes
	// later.
	generation uint64
}

// virtualPrefetchKey is the equivalence key for prefetch deduplication. Two
// requests are equivalent only when they name the same source (content identity
// plus provider-neutral URI plus owning installation) for the same viewer
// profile (account and profile). Any difference is a distinct prefetch: a
// different release under the same content, or the same content for a different
// profile, must not be collapsed.
func virtualPrefetchKey(contentID, neutralURI string, ownerInstallationID, userID int, profileID string) string {
	return contentID + "\x00" + neutralURI + "\x00" + strconv.Itoa(ownerInstallationID) + "\x00" + strconv.Itoa(userID) + "\x00" + profileID
}

// spawnVirtualCandidateSink persists a freshly listed first-playback candidate
// set in the background and, only when that persist succeeds, announces the new
// version list on the same catalog event the Refresh List executor emits.
//
// The persist is admitted through the handler's bounded detached-work gate and
// runs on a detached, budgeted context. Admission is non-blocking and
// drop-on-exhaustion: on a saturated gate the sink is skipped entirely (logged
// at debug) and no event is published, so a later first open of the title
// reloads the list instead of being announced from work that never ran. The
// event is deliberately gated on a successful persist so a client never reloads
// a version list that was not written; a persist error is a no-op.
func (h *PlaybackHandler) spawnVirtualCandidateSink(ctx context.Context, file *models.MediaFile, streams []VirtualPlaybackStream) {
	if h == nil || h.VirtualPlaybackStreamSink == nil || file == nil || len(streams) == 0 {
		return
	}
	sinkFn := h.VirtualPlaybackStreamSink
	sinkFile := *file
	contentID := file.ContentID
	eventsHub := h.EventsHub
	gate := h.detachedGate()
	if !gate.tryAcquire() {
		slog.DebugContext(ctx, "virtual playback stream sink skipped: detached worker budget exhausted",
			"component", "api")
		return
	}
	sinkCtx, sinkCancel := h.virtualDetachedContext(ctx, virtualCandidateSinkBudget)
	go func() {
		defer gate.release()
		defer sinkCancel()
		if err := sinkFn(sinkCtx, &sinkFile, streams); err != nil {
			return
		}
		// The sink persisted the first-playback candidates: new rows are now
		// selectable versions. Publish only after the write lands, matching the
		// executor's post-persist ordering, so an open detail or watch page
		// stops showing the seeded placeholder.
		//
		// The publish gets its own short context created here, after success,
		// rather than reusing sinkCtx. A slow-but-successful persist can spend
		// the whole sink budget, and a publish on the drained sinkCtx would then
		// fan out locally (hub.go publishes local subscribers before the bus)
		// while the Redis publish that refreshes the other API nodes fails. The
		// fresh context is detached from request cancellation but stays parented
		// to service shutdown, so it keeps the sink's lifecycle guarantees.
		publishCtx, publishCancel := h.virtualDetachedContext(ctx, virtualCandidatePublishBudget)
		defer publishCancel()
		publishVirtualVersionsUpdatedEvent(publishCtx, nil, eventsHub, contentID)
	}()
}

// PrefetchVirtualPlayback warms the metadata caches for up to
// maxVirtualPlaybackPrefetchFiles virtual files, best-effort.
//
// Work is admitted into a process-wide bounded queue before any goroutine
// handles it and deduplicated by virtualPrefetchKey, so duplicate requests
// collapse while distinct-source floods are rejected instead of spawning
// goroutines. A fixed worker pool bounds active work; the queue bounds pending
// work; the dedup map is bounded because an entry exists only while its task is
// pending or active. Under overload the request is shed with a debug log and a
// false admission, never queued without bound and never blocking the caller.
// Each admitted task runs under a context bound to the prefetch budget and the
// service lifecycle, and the work is metadata-only: it warms the listing and
// resolver caches and never pins sticky playback evidence.
func (h *PlaybackHandler) PrefetchVirtualPlayback(ctx context.Context, files []*models.MediaFile, profileID string) {
	if h == nil || len(files) == 0 || profileID == "" {
		return
	}
	if h.VirtualPlaybackResolver == nil && h.VirtualPlaybackStreamLister == nil {
		return
	}
	userID := apimw.GetUserID(ctx)
	if userID == 0 {
		return
	}
	if len(files) > maxVirtualPlaybackPrefetchFiles {
		files = files[:maxVirtualPlaybackPrefetchFiles]
	}
	h.startVirtualPrefetchWorkers()
	for _, file := range files {
		if file == nil || !isVirtualPlaybackFile(file) {
			continue
		}
		neutralURI := virtualPlaybackNeutralKey(file.FilePath)
		key := virtualPrefetchKey(file.ContentID, neutralURI, file.VirtualOwnerInstallationID, userID, profileID)
		if !h.admitVirtualPrefetch(key) {
			continue
		}
		task := virtualPrefetchTask{
			file:       *file,
			neutralURI: neutralURI,
			userID:     userID,
			profileID:  profileID,
			key:        key,
			generation: nextVirtualCacheGeneration(),
		}
		select {
		case h.prefetchQueue <- task:
		default:
			// The queue filled between admission and enqueue (a worker burst
			// drained and refilled it). Undo the dedup entry so a later request
			// can be admitted, and drop this one loudly but without blocking.
			h.releaseVirtualPrefetch(key)
			slog.DebugContext(ctx, "virtual playback prefetch rejected: pending queue full",
				"component", "api", "queue_size", virtualPrefetchQueueSize)
			observeVirtualPrefetchOutcome(virtualPrefetchQueueFull)
		}
	}
}

// startVirtualPrefetchWorkers lazily starts the fixed prefetch worker pool. The
// pool is bounded (virtualPrefetchWorkers) so no request can spawn a goroutine.
func (h *PlaybackHandler) startVirtualPrefetchWorkers() {
	h.prefetchOnce.Do(func() {
		if h.prefetchQueue == nil {
			h.prefetchQueue = make(chan virtualPrefetchTask, virtualPrefetchQueueSize)
		}
		if h.prefetchInFlight == nil {
			h.prefetchInFlight = make(map[string]struct{})
		}
		for range virtualPrefetchWorkers {
			go h.runVirtualPrefetchWorker()
		}
	})
}

// admitVirtualPrefetch records an in-flight key and reports whether admission
// succeeded. It rejects when the dedup set is at its bound, which is the same
// bound as pending+active work.
func (h *PlaybackHandler) admitVirtualPrefetch(key string) bool {
	h.prefetchMu.Lock()
	defer h.prefetchMu.Unlock()
	if h.prefetchStopped {
		observeVirtualPrefetchOutcome(virtualPrefetchShutdown)
		return false
	}
	if h.prefetchInFlight == nil {
		h.prefetchInFlight = make(map[string]struct{})
	}
	if _, dup := h.prefetchInFlight[key]; dup {
		observeVirtualPrefetchOutcome(virtualPrefetchDuplicate)
		return false
	}
	if len(h.prefetchInFlight) >= virtualPrefetchQueueSize+virtualPrefetchWorkers {
		slog.Debug("virtual playback prefetch rejected: in-flight set full",
			"component", "api", "bound", virtualPrefetchQueueSize+virtualPrefetchWorkers)
		observeVirtualPrefetchOutcome(virtualPrefetchInflightFull)
		return false
	}
	h.prefetchInFlight[key] = struct{}{}
	observeVirtualPrefetchOutcome(virtualPrefetchAdmitted)
	return true
}

func (h *PlaybackHandler) releaseVirtualPrefetch(key string) {
	h.prefetchMu.Lock()
	delete(h.prefetchInFlight, key)
	h.prefetchMu.Unlock()
}

// runVirtualPrefetchWorker drains admitted prefetch tasks until the service
// context ends. Queued work at shutdown is abandoned with the process; the
// dedup set is cleared so nothing is retained past shutdown.
func (h *PlaybackHandler) runVirtualPrefetchWorker() {
	var serviceDone <-chan struct{}
	if h.ServiceContext != nil {
		serviceDone = h.ServiceContext.Done()
	}
	for {
		select {
		case <-serviceDone:
			h.prefetchMu.Lock()
			h.prefetchStopped = true
			if n := len(h.prefetchInFlight); n > 0 {
				slog.Warn("virtual playback prefetch abandoned at shutdown",
					"component", "api", "in_flight", n)
			}
			h.prefetchInFlight = make(map[string]struct{})
			h.prefetchMu.Unlock()
			return
		case task := <-h.prefetchQueue:
			h.prefetchOne(task)
			h.releaseVirtualPrefetch(task.key)
		}
	}
}

// prefetchOne runs one admitted task: listing warm only, under its own
// budget and the service lifecycle, gated by the aggregate detached gate so a
// prefetch burst cannot exceed the shared bound. It never pins sticky evidence.
//
// The prefetch is list-only by design: it warms the shared resolver candidate
// cache and the handler best-result cache, and never touches a candidate's
// media URL (no resolve, no anchor probe, no range fetch, no ffprobe). Opening
// a ProviderURL can activate the provider (an AltMount Stremio link starts
// fetching on first access), so a bytes-touching warm belongs to an explicit
// version selection (ScheduleSelectedFileWarm) or an explicit Play, never to a
// browse-driven prefetch.
func (h *PlaybackHandler) prefetchOne(task virtualPrefetchTask) {
	gate := h.detachedGate()
	if !gate.tryAcquire() {
		slog.Debug("virtual playback prefetch skipped: detached worker budget exhausted",
			"component", "api")
		observeVirtualPrefetchOutcome(virtualPrefetchGateExhausted)
		return
	}
	defer gate.release()
	prefetchCtx, cancel := h.virtualDetachedContext(h.ServiceContext, virtualPlaybackPrefetchBudget)
	defer cancel()
	if prefetchCtx.Err() != nil {
		observeVirtualPrefetchOutcome(virtualPrefetchContextDone)
		return
	}
	// Listing warms the shared resolver candidate cache and stores the
	// device-neutral candidate set in the handler cache, so the first click
	// skips the provider round-trip. Metadata-only; no media URL is opened.
	h.warmVirtualPlaybackListing(prefetchCtx, &task.file, task.neutralURI, task.userID, task.profileID, task.generation)
}

// warmVirtualPlaybackListing lists provider candidates once and stores the
// filtered, device-neutral set in the best-result cache under the neutral key
// (no device fingerprint). A later start for any device falls back to that
// entry, ranks it for the requesting device, and skips the provider list. It is
// best-effort: the caller owns the prefetch budget and this function ignores
// provider errors. Only the metadata cache is warmed here; the sticky pin is
// deliberately not set, because a candidate that has never delivered bytes is
// not yet evidence it should steer starts.
func (h *PlaybackHandler) warmVirtualPlaybackListing(ctx context.Context, file *models.MediaFile, neutralURI string, userID int, profileID string, generation uint64) {
	if h == nil || file == nil || neutralURI == "" {
		return
	}
	if h.VirtualPlaybackStreamLister == nil || h.BestResultCache == nil {
		return
	}
	// Admitted work is bound to a deadline and the service lifecycle; an
	// already-expired context must not start a provider round-trip.
	if ctx != nil && ctx.Err() != nil {
		return
	}
	streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(ctx, neutralURI, userID, profileID, file.VirtualOwnerInstallationID)
	if err != nil {
		observeVirtualPrefetchOutcome(virtualPrefetchListError)
		return
	}
	if len(streams) == 0 {
		observeVirtualPrefetchOutcome(virtualPrefetchEmpty)
		return
	}
	if len(streams) > maxVirtualPlaybackStreams {
		streams = streams[:maxVirtualPlaybackStreams]
	}
	// Filter against the neutral row, exactly as a start would, so the cached
	// set is the same one the start path would persist.
	base := *file
	base.FilePath = neutralURI
	filtered := filterVirtualPlaybackStreams(&base, streams)
	if len(filtered) == 0 {
		observeVirtualPrefetchOutcome(virtualPrefetchFilteredEmpty)
		return
	}
	h.BestResultCache.setWithDetailsAt(
		bestResultCacheKey(file.ContentID, neutralURI, file.VirtualOwnerInstallationID),
		file.ContentID, neutralURI, file.VirtualOwnerInstallationID,
		filtered, time.Now(), generation,
	)
	observeVirtualPrefetchOutcome(virtualPrefetchCompleted)
}

func (h *PlaybackHandler) maxVirtualFailoverAttempts(ctx context.Context) int {
	if h != nil && h.SettingsRepo != nil {
		if raw, err := h.SettingsRepo.Get(ctx, "playback.max_virtual_failover_attempts"); err == nil && strings.TrimSpace(raw) != "" {
			if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
				return v
			}
		}
	}
	if h != nil && h.PlaybackConfig != nil {
		if v := h.playbackConfig().MaxVirtualFailoverAttempts; v > 0 {
			return v
		}
	}
	return defaultMaxVirtualFailoverAttempts
}

// neutralFallbackCacheCause falls back to the device-neutral best-result
// entry when the fingerprinted lookup missed. It returns the streams to serve
// (possibly empty) and the miss cause to report. When both lookups miss it
// joins the causes so an absent device entry is not hidden behind a neutral
// one; when the neutral entry hits the cause is empty (no miss to explain).
// Observational only; callers decide nothing off the result.
func (h *PlaybackHandler) neutralFallbackCacheCause(cached []VirtualPlaybackStream, cause string, file *models.MediaFile, neutralURI string) ([]VirtualPlaybackStream, string) {
	neutralCached, neutralCause := h.BestResultCache.getWithCause(bestResultCacheKey(file.ContentID, neutralURI, file.VirtualOwnerInstallationID), time.Now())
	if len(neutralCached) > 0 {
		return neutralCached, ""
	}
	if cause != "" && neutralCause != "" {
		return nil, cause + "+neutral_" + neutralCause
	}
	if neutralCause != "" {
		return nil, neutralCause
	}
	return nil, cause
}

const (
	defaultBestResultCacheTTL     = 30 * time.Minute
	defaultBestResultCacheEntries = 512
)

// virtualCacheGeneration is the process-wide monotonic generation source for
// the best-result cache and the sticky pin. Each resolve (and each prefetch
// task) captures a generation before it starts work and passes it to its cache
// and pin writes; a write whose generation is older than the entry already
// stored is refused, so a slow resolve cannot overwrite the result of a newer
// one that finished first. The counter is process-local and never persisted,
// exactly like the caches it fences.
var virtualCacheGeneration atomic.Uint64

func nextVirtualCacheGeneration() uint64 {
	return virtualCacheGeneration.Add(1)
}

// VirtualBestResultCache remembers which result= URI worked for a content+profile
// pair. On replay it skips the list+resolve+probe path entirely, jumping
// directly to the known-good provider-neutral URI.
type VirtualBestResultCache struct {
	mu         sync.RWMutex
	entries    map[string]bestResultCacheEntry
	ttl        time.Duration
	maxEntries int
}

type bestResultCacheEntry struct {
	contentID           string
	neutralURI          string
	ownerInstallationID int
	streams             []VirtualPlaybackStream
	expiresAt           time.Time
	// generation is the write generation that produced this entry. A later
	// writer carrying an older generation must not replace it.
	generation uint64
}

// NewVirtualBestResultCache returns an initialized cache. Zero or negative ttl
// and maxEntries pick safe defaults.
func NewVirtualBestResultCache(ttl time.Duration, maxEntries int) *VirtualBestResultCache {
	if ttl <= 0 {
		ttl = defaultBestResultCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultBestResultCacheEntries
	}
	return &VirtualBestResultCache{
		entries:    make(map[string]bestResultCacheEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

func (c *VirtualBestResultCache) get(key string, now time.Time) []VirtualPlaybackStream {
	streams, _ := c.getWithCause(key, now)
	return streams
}

// getWithCause is get plus the miss cause a caller needs to size the
// convertible share of cold starts: it distinguishes "no entry at all" from
// "an entry that lapsed its TTL". Observational only; callers decide nothing
// off the cause. A nil cache reports cache_disabled rather than a fabricated
// hit or miss.
func (c *VirtualBestResultCache) getWithCause(key string, now time.Time) ([]VirtualPlaybackStream, string) {
	if c == nil {
		return nil, "cache_disabled"
	}
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return nil, "absent"
	}
	if now.After(entry.expiresAt) {
		return nil, "expired"
	}
	return append([]VirtualPlaybackStream(nil), entry.streams...), ""
}

// Clear drops every cached result. Called on plugin lifecycle changes when
// provider configurations may have changed and cached result= URIs are
// likely stale.
func (c *VirtualBestResultCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

func (c *VirtualBestResultCache) RemoveCandidate(key string, candURI string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return
	}
	candNeutral := virtualPlaybackNeutralKey(candURI)
	filtered := make([]VirtualPlaybackStream, 0, len(entry.streams))
	for _, s := range entry.streams {
		if s.URI == candURI || (candNeutral != "" && s.URI == candNeutral) {
			continue
		}
		filtered = append(filtered, s)
	}
	if len(filtered) == 0 {
		delete(c.entries, key)
		return
	}
	entry.streams = filtered
	c.entries[key] = entry
}

func (c *VirtualBestResultCache) RemoveCandidateForContent(contentID, neutralURI string, ownerInstallationID int, candURI string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	candNeutral := virtualPlaybackNeutralKey(candURI)
	targetCandID := ""
	if parsed, err := url.Parse(candURI); err == nil {
		targetCandID = parsed.Query().Get("result")
	}
	for k, entry := range c.entries {
		if (contentID == "" || entry.contentID == contentID) &&
			(neutralURI == "" || entry.neutralURI == neutralURI) &&
			entry.ownerInstallationID == ownerInstallationID {
			filtered := make([]VirtualPlaybackStream, 0, len(entry.streams))
			for _, s := range entry.streams {
				if s.URI == candURI || (candNeutral != "" && s.URI == candNeutral) || (targetCandID != "" && s.ID == targetCandID) {
					continue
				}
				filtered = append(filtered, s)
			}
			if len(filtered) == 0 {
				delete(c.entries, k)
			} else {
				entry.streams = filtered
				c.entries[k] = entry
			}
		}
	}
}

func (c *VirtualBestResultCache) set(key string, streams []VirtualPlaybackStream, now time.Time) {
	c.setWithDetails(key, "", "", 0, streams, now)
}

func (c *VirtualBestResultCache) setWithDetails(key, contentID, neutralURI string, ownerInstallationID int, streams []VirtualPlaybackStream, now time.Time) {
	c.setWithDetailsAt(key, contentID, neutralURI, ownerInstallationID, streams, now, nextVirtualCacheGeneration())
}

// setWithDetailsAt is setWithDetails with an explicit write generation. It
// refuses to replace an entry written by a newer generation, so a slow resolve
// or detached prefetch that finishes after a newer one cannot overwrite it.
func (c *VirtualBestResultCache) setWithDetailsAt(key, contentID, neutralURI string, ownerInstallationID int, streams []VirtualPlaybackStream, now time.Time, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[key]; ok && existing.generation > generation {
		return
	}
	for k, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= c.maxEntries {
		var oldestKey string
		var oldest time.Time
		for k, entry := range c.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = k, entry.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = bestResultCacheEntry{
		contentID:           contentID,
		neutralURI:          neutralURI,
		ownerInstallationID: ownerInstallationID,
		streams:             streams,
		expiresAt:           now.Add(c.ttl),
		generation:          generation,
	}
}

// bestResultCacheKey builds a deterministic key from the content_id, neutral
// URI (without result=), and owner installation ID, with an optional device fingerprint.
func bestResultCacheKey(contentID, neutralURI string, ownerInstallationID int, deviceFingerprint ...string) string {
	raw := contentID + "\x00" + neutralURI + "\x00" + strconv.Itoa(ownerInstallationID)
	if len(deviceFingerprint) > 0 && deviceFingerprint[0] != "" {
		raw += "\x00" + deviceFingerprint[0]
	}
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:16])
}

type VirtualPlaybackResolver interface {
	ResolveVirtualPlayback(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) (string, error)
}

type VirtualPlaybackResolverFunc func(context.Context, string, int, string, int) (string, error)

func (f VirtualPlaybackResolverFunc) ResolveVirtualPlayback(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) (string, error) {
	return f(ctx, path, userID, profileID, ownerInstallationID)
}

// VirtualPlaybackStream is the provider-neutral candidate shape used by the
// just-in-time picker. Implementations must never expose provider URLs here.
type VirtualPlaybackStream struct {
	ID                  string            `json:"id"`
	Label               string            `json:"label"`
	URI                 string            `json:"uri"`
	Resolution          string            `json:"resolution,omitempty"`
	CodecVideo          string            `json:"codec_video,omitempty"`
	CodecAudio          string            `json:"codec_audio,omitempty"`
	HDR                 string            `json:"hdr,omitempty"`
	SourceType          string            `json:"source_type,omitempty"`
	FileSize            int64             `json:"file_size,omitempty"`
	Container           string            `json:"container,omitempty"`
	Bitrate             int               `json:"bitrate,omitempty"`
	FrameRate           string            `json:"frame_rate,omitempty"`
	AudioLanguages      []string          `json:"audio_languages,omitempty"`
	SubtitleLanguages   []string          `json:"subtitle_languages,omitempty"`
	AudioChannels       string            `json:"audio_channels,omitempty"`
	HasAtmos            bool              `json:"has_atmos,omitempty"`
	Is10Bit             bool              `json:"is_10bit,omitempty"`
	IsMultiAudio        bool              `json:"is_multi_audio,omitempty"`
	VisualTags          []string          `json:"visual_tags,omitempty"`
	AudioTags           []string          `json:"audio_tags,omitempty"`
	QualityScore        int               `json:"quality_score,omitempty"`
	RequestHeaders      map[string]string `json:"-"`
	OwnerInstallationID int               `json:"-"`
	Visible             bool              `json:"-"`
	VisibilitySpecified bool              `json:"-"`

	// PreserveProviderOrder carries the provider-order policy through caches and pickers.
	PreserveProviderOrder bool `json:"-"`

	// Rejected marks a candidate a configured custom format rejects. It is a
	// transient ranking signal: accepted candidates always sort before
	// rejected ones, and the final ordering partition keeps a rejected
	// candidate behind every accepted one so device fit can never promote it
	// to the front. Rejected remains last-resort selectable.
	Rejected bool `json:"-"`
	// ProviderURL, ProviderVideoHash, ProviderGUID and ProviderReleaseName
	// carry the lister's durable provider identity to the persistence sink.
	// ProviderURL is internal only and must never be serialized to a client;
	// every field is json:"-" and the sink is the sole consumer.
	ProviderURL         string `json:"-"`
	ProviderVideoHash   string `json:"-"`
	ProviderGUID        string `json:"-"`
	ProviderReleaseName string `json:"-"`
	// ProviderExpiresAt is the provider URL's parsed query-expiry (nil when
	// unparseable). It is the expiry the persistence sink stores alongside
	// ProviderURL.
	ProviderExpiresAt *time.Time `json:"-"`
}

// Get* accessors satisfy plugins.VirtualStreamMetadata so the shared device
// ranker can score both this type and the plugin-layer candidate shape.
func (s VirtualPlaybackStream) GetCodecVideo() string { return s.CodecVideo }
func (s VirtualPlaybackStream) GetCodecAudio() string { return s.CodecAudio }
func (s VirtualPlaybackStream) GetHDR() string        { return s.HDR }
func (s VirtualPlaybackStream) GetContainer() string  { return s.Container }
func (s VirtualPlaybackStream) GetResolution() string { return s.Resolution }
func (s VirtualPlaybackStream) GetHasAtmos() bool     { return s.HasAtmos }
func (s VirtualPlaybackStream) GetQualityScore() int  { return s.QualityScore }

type VirtualPlaybackStreamLister interface {
	ListVirtualPlaybackStreams(ctx context.Context, virtualPath string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error)
}

type VirtualPlaybackStreamListerFunc func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error)

func (f VirtualPlaybackStreamListerFunc) ListVirtualPlaybackStreams(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) ([]VirtualPlaybackStream, error) {
	return f(ctx, path, userID, profileID, ownerInstallationID)
}

// VirtualPlaybackStreamSink persists JIT candidates as selectable virtual
// files.
type VirtualPlaybackStreamSink func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error

type ProbeProvenance string

const (
	ProbeProvenanceDeclared ProbeProvenance = "declared"
	ProbeProvenancePending  ProbeProvenance = "pending"
	ProbeProvenancePartial  ProbeProvenance = "partial"
	ProbeProvenanceVerified ProbeProvenance = "verified"
	ProbeProvenanceFailed   ProbeProvenance = "failed"
)

type resolvedVirtualPlaybackSource struct {
	URL               string
	URI               string
	OwnerID           int
	File              *models.MediaFile
	ProbeSucceeded    bool
	Provenance        ProbeProvenance
	ResolutionAssumed bool
	// ResolvedURL is the validated provider URL the resolver returned, with
	// its parsed expiry and the candidate's durable identity. They are
	// additive evidence the adoption path persists on the catalog row so a
	// later phase can reuse the URL instead of re-listing.
	ResolvedURL          string
	ResolvedURLExpiresAt *time.Time
	ProviderVideoHash    string
	ProviderGUID         string
	ProviderReleaseName  string
	ProviderReleaseSize  int64
	// RequestHeaders is the relay-forwardable header set (Referer, Origin,
	// User-Agent) the resolved URL needs. It is persisted with the URL so a
	// header-authenticated provider stream stays usable from the catalog.
	RequestHeaders map[string]string
	// CandidateRank is the 0-based position of the selected candidate in the
	// ranked candidate list this resolve considered, or -1 when the resolve
	// took a path that did not rank (a stored-URL fast path with no list).
	// CandidateCount is that list's length: the count the selected rank is
	// measured against, or -1 when unknown. A measured zero comes from an
	// empty ranked list (or, defensively, from any path that never set the
	// field); it is not a rank into a list and must render without one.
	// They feed the plan-decision log so a deployment question ("which
	// candidate did it pick, and out of how many?") is answerable without a
	// second resolve.
	CandidateRank  int
	CandidateCount int
	// SubstitutedFromFileID names the catalog row the caller asked for when the
	// resolver's candidate belongs to a different row. It is nonzero only for
	// the alternate-version walk, which is the one fresh-start path that plans
	// a release other than the requested one.
	SubstitutedFromFileID int
	// SubstitutionReason is the additive substitution cause for that case.
	SubstitutionReason string
	// IdentityRematched is true when the resolver reported that the requested
	// pin's result id was absent from a fresh listing but the same durable
	// identity was found under a new result id. The candidate is then the same
	// release re-identified, not a substitution, and a rotation that requires
	// release continuity may accept it.
	IdentityRematched bool
	// DeferredProbe, when non-nil, is the probe the fresh-start resolve wants
	// run after the transport commits and the first-byte URL is handed back.
	// The resolve deliberately does not spawn it inline: session creation,
	// recipe persistence, and transport commit run first so the client gets a
	// playable URL without waiting on ffprobe enumeration, and the start path
	// spawns the probe the moment the URL exists. Nil for a resolve that probed
	// synchronously or had nothing to probe.
	DeferredProbe *virtualDeferredProbeV3
	// IncompleteEnumeration marks a resolved source that settled on the
	// candidate's declared metadata because its synchronous probe returned a
	// structurally incomplete enumeration (scanner.ErrVirtualProbePartial)
	// rather than a verdict about the bytes. The candidate keeps its pin and
	// the failure damper is not consumed, but the resolve does not scan
	// trailing candidates for a verified sibling: an unknown-verdict candidate
	// is never substituted. The caller returns the declared disposition
	// immediately, exactly as it does for a pending deferred lifecycle. The
	// retry across starts is bounded by the incomplete-enumeration streak.
	IncompleteEnumeration bool
	// EvidenceObservation marks a probed source whose bytes only recorded the
	// pin's first served-size observation. The source is served with the probed
	// inventory, so its menus are real, but the caller must not persist, adopt,
	// or stamp it: only a later serve that matches the observation establishes
	// the pin (see virtualProbeEvidenceWithholdsIdentity).
	EvidenceObservation bool
}

// virtualProbeIdentity is the durable provider identity of the candidate a
// resolve actually returned. It travels with the resolved source so every
// adoption write persists the identity of the bytes being adopted, not just
// the probed track inventory. Without it a cross-release adoption cleared the
// row's identity (the replace-on-adoption SQL writes the supplied tiers) and a
// later re-list could not re-identify the same release.
type virtualProbeIdentity struct {
	VideoHash   string
	GUID        string
	ReleaseName string
	ReleaseSize int64
}

// applyResolvedIdentity stamps the resolved candidate's durable identity onto
// the file being served and persisted. It fills empty tiers only, never
// overwriting stored identity, so a legacy row gains re-match capability
// without risking a cross-release overwrite.
func applyResolvedIdentity(transient *models.MediaFile, id virtualProbeIdentity) {
	if transient == nil {
		return
	}
	if transient.ProviderVideoHash == "" {
		transient.ProviderVideoHash = id.VideoHash
	}
	if transient.ProviderGUID == "" {
		transient.ProviderGUID = id.GUID
	}
	if transient.ProviderReleaseName == "" {
		transient.ProviderReleaseName = id.ReleaseName
	}
	if transient.ProviderReleaseSize <= 0 {
		transient.ProviderReleaseSize = id.ReleaseSize
	}
}

// tiers splits the resolver's reported candidate identity into the comparison
// tiers SharedTier consumes. It is the bridge from the resolver's own evidence
// about what it served to the release-continuity decision, which must never be
// read off the served transient (that file is seeded from the requested row; see
// withResolvedCandidate).
func (id virtualProbeIdentity) tiers() resolver.PersistedIdentityTiers {
	return resolver.NewPersistedIdentityTiers(id.VideoHash, id.GUID, id.ReleaseName, id.ReleaseSize)
}

// resolvedSourceIdentity returns the resolver's own report of the candidate it
// returned, captured on the resolved source by withResolvedCandidate. An empty
// identity means the resolver proved nothing about the served bytes, which the
// release-continuity checks treat as a refusal rather than a match.
func resolvedSourceIdentity(src *resolvedVirtualPlaybackSource) virtualProbeIdentity {
	if src == nil {
		return virtualProbeIdentity{}
	}
	return virtualProbeIdentity{
		VideoHash:   src.ProviderVideoHash,
		GUID:        src.ProviderGUID,
		ReleaseName: src.ProviderReleaseName,
		ReleaseSize: src.ProviderReleaseSize,
	}
}

func virtualFileHasPlayableDeclaredMetadata(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	return file.Resolution != "" && file.CodecVideo != "" && file.CodecAudio != "" && canSkipProbeForContainer(file.Container)
}

// fastPathGateEligible reports whether the repeat-play fast paths below can
// apply at all: they are gated on the deferred start branch with no forced
// relist, an explicit candidate, and no exclusions. It is a coarse,
// observational-only input to the list_reason trace field: a start that is
// ineligible here may still take a fast path later (durable resume,
// delivery grace), so the field records intent, not outcome.
func fastPathGateEligible(deferProbe, forceRelist, noResult bool, excludedCandidateIDs []string) bool {
	return deferProbe && !forceRelist && !noResult && len(excludedCandidateIDs) == 0
}

// shouldListVirtualPlaybackCandidates reports whether the resolver must ask
// the provider for a candidate list. A pinned result= URI with complete
// probed evidence normally skips the round-trip. forceRelist overrides that so
// an explicit retry of an unavailable version sees the provider's current list
// rather than re-trying the stale pinned candidate.
func shouldListVirtualPlaybackCandidates(noResult, needsCandidateMetadata, forceRelist bool) bool {
	return noResult || needsCandidateMetadata || forceRelist
}

// virtualResolveTrace accumulates the wall-clock cost of each phase inside
// resolveVirtualPlaybackSource. The protocol v3 start timings collapse all of
// this into a single file_load_probe mark; these attrs split it into the
// provider candidate list, the provider resolve loop, the
// synchronous probe, and the stale-source re-list fallback, so a cold-start
// attribution can name the dominant stage instead of guessing. It is
// observational only and never gates control flow.
//
// A stage that did not run must be distinguishable from one that ran in under
// a millisecond: every stage carries a <name>_ran boolean, and its <name>_ms
// duration is omitted entirely unless it ran. A plain 0 duration can therefore
// only mean "ran in under 1 ms", never "skipped". total_ms is the sum of the
// durations that ran, so the displayed stage fields add up to it exactly;
// elapsed_ms is the wall-clock time of the whole resolve.
type virtualResolveTrace struct {
	started time.Time

	list    time.Duration
	listRan bool

	// fetch is the provider discovery time the resolve actually paid: the sum
	// of the provider listing attempts the resolver performed on this request's
	// context. It is measured by the resolver's provider-fetch observer rather
	// than inferred from the enclosing stage, and it is excluded from list and
	// resolve so the stages below do not double-count the same wall time.
	// fetchRan distinguishes "no discovery" from "discovery in under a
	// millisecond", exactly like the other stages.
	fetch    time.Duration
	fetchRan bool
	// fetchCalls counts the provider listing attempts this resolve performed.
	// A repeated start with a reusable resolution reports zero; a concurrent
	// start that shared another request's flight also reports zero because the
	// observer only fires for a fetch this request's context initiated.
	fetchCalls int

	resolve    time.Duration
	resolveRan bool

	probe    time.Duration
	probeRan bool

	fallback    time.Duration
	fallbackRan bool

	// budgetMS is the cold-path budget in milliseconds the trace ran under
	// (virtualStartupBudget at trace start). budgetExceeded is set when the
	// wall-clock resolve time passed it: with every stage deriving from the
	// single cold deadline, an overrun means the deadline was not honored
	// end to end, and the line tells exactly which stages consumed it.
	budgetMS      int64
	budgetRan     bool
	budgetExceed  bool
	budgetElapsed time.Duration

	fastPath   bool
	cached     bool
	listed     bool
	candidates int
	// listReason names why the listing gate decided the way it did. It is
	// observational only and never gates control flow. Values:
	//   fast_path (the repeat-play fast path was eligible at gate time:
	//     deferred start, explicit candidate, no relist, no exclusions),
	//   cache_hit (best-result cache reused, list skipped),
	//   resumed (durable-resume hit, list skipped like a cache hit),
	//   explicit_declared (explicit ?result= with declared metadata, instant path),
	//   empty_suppressed (provider answered empty inside the window),
	//   listed (provider round-trip ran; see the *_ms stages for cost),
	//   unknown (no early gate claimed the start; a late fast path inside the
	//     attempt loop may still have skipped the provider — see fast_path —
	//     and any other shape stays unknown rather than a fabricated cause).
	//
	// fast_path eligibility is intent, not outcome: a start can be
	// fast-path-eligible at the gate, reuse the cache (cache_hit=true), and
	// still take the delivery-grace fast path in the attempt loop. The
	// outcome fields (fast_path, cache_hit, listed) always describe what
	// ran; list_reason describes which early gate claimed the start.
	listReason string
	// cacheMissCause sizes the convertible share of "listed" starts: why the
	// best-result cache did not serve this start. It is observational only and
	// never gates control flow. Values are bounded:
	//   none (the start did not list; no miss to explain),
	//   absent (no cache entry for either the fingerprinted or neutral key),
	//   expired (an entry existed but was past its TTL),
	//   forced_relist (an explicit relist bypassed a usable cache),
	//   exclusion_or_unusable (an exclusion or failed-verdict rotation
	//     required a fresh list past the cache),
	//   explicit (an explicit ?result= row never consults the neutral cache),
	//   cache_disabled (no BestResultCache configured),
	//   unknown (any other shape).
	cacheMissCause string
}

// totalMS is the sum of the stage durations that ran, using the same rounded
// millisecond values the per-stage fields report, so total_ms equals
// list_ms+fetch_ms+resolve_ms+probe_ms+fallback_ms exactly. fetch is a stage
// carved out of list and resolve (the enclosing stages are recorded net of the
// discovery they paid), so adding it does not double-count provider time.
func (t *virtualResolveTrace) totalMS() int64 {
	return t.list.Milliseconds() +
		t.fetch.Milliseconds() +
		t.resolve.Milliseconds() +
		t.probe.Milliseconds() +
		t.fallback.Milliseconds()
}

// finishBudget records the trace's budget verdict at log time: the elapsed
// wall time against the budget captured at trace start. Called once, from
// the deferred log, so cancellation paths report the same verdict as
// success paths.
func (t *virtualResolveTrace) finishBudget() {
	if t == nil || t.budgetRan {
		return
	}
	t.budgetRan = true
	t.budgetElapsed = time.Since(t.started)
	t.budgetExceed = t.budgetMS > 0 && t.budgetElapsed > time.Duration(t.budgetMS)*time.Millisecond
}

// fields returns the timing shape in a stable order. It is split out from log
// so tests can assert the "ran" verdict per stage without capturing the logger.
// elapsed_ms reuses the frozen budget verdict's elapsed value (finishBudget
// runs first in log), so the reported elapsed and the exceeded verdict can
// never disagree near the boundary.
func (t *virtualResolveTrace) fields() []any {
	return t.fieldsWithContext(context.Background())
}

// fieldsWithContext is fields plus the edge request id, read from the start
// request's context. It joins this line to the start that owns it (protocol
// v3 start timing) and to the provider listing it may have triggered
// (provider candidates fetched). Empty stays empty rather than a fabricated
// join key. Split out so tests can call fields() without a request context.
func (t *virtualResolveTrace) fieldsWithContext(ctx context.Context) []any {
	elapsedMS := t.budgetElapsed.Milliseconds()
	if !t.budgetRan {
		elapsedMS = time.Since(t.started).Milliseconds()
	}
	attrs := []any{
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"elapsed_ms", elapsedMS,
		"total_ms", t.totalMS(), //nolint:goconst // log attribute key/value, kept inline for readability.
		"candidates", t.candidates, //nolint:goconst // log attribute key/value, kept inline for readability.
		"fetch_calls", t.fetchCalls,
		"cache_hit", t.cached,
		"listed", t.listed,
		"fast_path", t.fastPath,
		"list_reason", t.listReason,
		"cache_miss_cause", t.cacheMissCause,
	}
	if t.budgetRan {
		attrs = append(attrs, "budget_ms", t.budgetMS)
		attrs = append(attrs, "budget_exceeded", t.budgetExceed)
	}
	for _, stage := range [...]struct {
		name string
		ran  bool
		d    time.Duration
	}{
		{"list", t.listRan, t.list},          //nolint:goconst // log attribute key/value, kept inline for readability.
		{"fetch", t.fetchRan, t.fetch},       //nolint:goconst // log attribute key/value, kept inline for readability.
		{"resolve", t.resolveRan, t.resolve}, //nolint:goconst // log attribute key/value, kept inline for readability.
		{"probe", t.probeRan, t.probe},
		{"fallback", t.fallbackRan, t.fallback},
	} {
		attrs = append(attrs, stage.name+"_ran", stage.ran)
		if stage.ran {
			attrs = append(attrs, stage.name+"_ms", stage.d.Milliseconds())
		}
	}
	return attrs
}

func (t *virtualResolveTrace) log(ctx context.Context, file *models.MediaFile) {
	if t == nil || file == nil {
		return
	}
	t.finishBudget()
	observeVirtualResolveListReason(t.listReason, t.cacheMissCause)
	attrs := []any{logComponentKey, "api", "content_id", file.ContentID} //nolint:goconst // log attribute key/value, kept inline for readability.
	attrs = append(attrs, t.fieldsWithContext(ctx)...)
	slog.InfoContext(ctx, "virtual resolve timing", attrs...)
}

// observeVirtualProviderFetches installs a provider-fetch observer on ctx and
// returns it with a snapshot function reporting the discovery duration and
// listing-attempt count observed so far. The observer fires only for fetches
// this request's context initiated: a fetch served from the resolver cache, or
// one this request merely waited on from another request's flight, reports
// nothing. The snapshot delta lets the enclosing stage (listing or resolution)
// report its wall time net of discovery, so the fetch stage names the provider
// cost exactly once instead of hiding it under a probe or resolve label.
//
// Every observed attempt also records its duration against the provider link,
// which is the measurement the copy-video seek-anchor resolve uses to size its
// own budget (see copySeekAnchorResolveBudget). It is link-local: one provider
// owner answering slowly is the pipe this source's anchor will read through,
// and the sample carries across requests for the lifetime of the window.
func observeVirtualProviderFetches(ctx context.Context, linkKey string) (context.Context, func() (time.Duration, int)) {
	var mu sync.Mutex
	var total time.Duration
	var calls int
	ctx = resolver.WithProviderFetchObserver(ctx, func(ev resolver.ProviderFetchEvent) {
		recordVirtualProviderLinkLatency(linkKey, ev.Duration, time.Now())
		mu.Lock()
		total += ev.Duration
		calls++
		mu.Unlock()
	})
	return ctx, func() (time.Duration, int) {
		mu.Lock()
		defer mu.Unlock()
		return total, calls
	}
}

// virtualStageNetElapsed is a stage's wall time minus the provider discovery
// it paid, floored at zero so a late observer cannot make a stage negative.
func virtualStageNetElapsed(start time.Time, discovery time.Duration) time.Duration {
	elapsed := time.Since(start) - discovery
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// resolveVirtualPlaybackSource chooses a ranked provider-neutral result,
// resolves it, and probes it before planning. A result URI is bound to the
// session so later Range, seek, subtitle, and transcode requests cannot silently
// switch to a technically different candidate under the original plan.
// resolveVirtualPlaybackSource resolves the provider URL for a virtual file
// and, when evidence is incomplete, probes it. deferProbe allows the start
// path to skip the synchronous probe and respond immediately on the
// deferred-metadata HLS route (see playback.DeferVirtualPlaybackMetadataV3);
// the resolved source is then probed in the background so the next play has
// complete evidence. Restart/track-change paths pass false because they need
// probed track inventory to remap selections.
//
// excludedCandidateIDs and preferredCandidateID are threaded into the detailed
// resolver so a replan can exclude the failed candidate and prefer the
// session-bound release instead of letting re-ranking drift to a different
// provider candidate. qualityPreference (already normalized) and
// bandwidthCapKbps steer the post-device-ranking reorder toward native
// lower-resolution candidates when the client asked for a fixed rung.
//
// forceRelist forces a fresh provider listing for an explicitly re-selected
// version that the catalog marked unavailable, instead of replaying the cached
// or pinned candidate. It also bypasses the detailed resolver's candidate cache
// so the retry sees the provider's current list. The pinned candidate is kept
// at index 0 while it is still listed; once the pin is gone the fresh list
// takes over.
//
// virtualResolveOptionsV3 carries the per-call intent of a detailed virtual
// resolve. It is variadic so the many existing callers keep their positional
// signature; production callers pass it explicitly.
type virtualResolveOptionsV3 struct {
	// allowFailedCandidate permits re-selecting a catalog row stamped failed_at.
	// False (the zero value) keeps an auto selection skipping a known-bad row,
	// even when the row already carries a concrete result= identity; true is
	// only for an explicit user retry, a forced relink, or a decode-rejection
	// rotation. A replan rehydration that is not a confirmed rotation must
	// leave it false so it honors the stamp and finds a live sibling instead
	// of looping back onto a candidate the serve layer already marked dead.
	allowFailedCandidate bool
	// rotateCandidates marks an exclusion as a deliberate candidate rotation: a
	// verdict that indicts the release (server-confirmed decode rejection) lets
	// the resolver substitute a sibling. When false, the resolver refuses to
	// fall past an excluded pinned candidate, so a display-driven fallback can
	// never silently swap the release.
	rotateCandidates bool
	// sessionBound declares that this resolve is for a release an existing
	// session is already serving (a replan rehydration or a serve-layer
	// re-resolve), as opposed to a fresh selection probing candidates. It is the
	// only reliable signal: both cases arrive as a ?result= in the URI, so the
	// presence of a preferred id cannot distinguish them. A session-bound
	// profile-removed pin refuses instead of swapping the release; a fresh
	// selection falls through to a profile-satisfying candidate.
	sessionBound bool
	// sessionAnchorURI is the immutable virtual source URI the playback session
	// is anchored to (the session manager's VirtualSourceURI). It is set only
	// for a session-bound replan rehydration. The stale-source fallback
	// validates the release it is about to refresh or substitute against this
	// anchor instead of trusting the persisted catalog row's current file_path,
	// which may have drifted while the session was serving.
	sessionAnchorURI string
	// explicitSelection declares that the requested row is an explicit user
	// version pick (protocol v3 file_selection=explicit). Like a session
	// binding, the viewer chose this exact persisted candidate, so the resolver
	// must not substitute a sibling release for it: a delisted same-identity
	// candidate is served from its persisted row inside the trust window and
	// refused (never swapped) when it is not. An auto selection leaves this
	// false and keeps the ordinary fallback/substitution behavior.
	explicitSelection bool
	// bypassProviderFloor marks a deliberate recovery resolve — an automatic
	// replan rotation or an alternate-version fallback — as a declared outage
	// re-list: it re-lists past the fresh-serve floor and the 30s
	// provider-failure fail-fast a recent background failure wrote, so recovery
	// is not eaten by that backoff. Background liveness pollers and ordinary
	// automatic resolves never set it and stay on the floor. The startup
	// budgets still bound the recovery, and exhaustion keeps its honest cause.
	bypassProviderFloor bool
	// deferProbePastCommit hands the candidate-declared upgrade probe back to
	// the caller instead of spawning it on the start path, so session creation,
	// recipe persistence, and the transport commit run before any ffprobe. Only
	// the fresh-start resolve sets it: its caller (the v3 start path) spawns the
	// probe once the first-byte URL exists and marks the plan tracks_pending.
	// A synchronous replan/rehydration leaves it false so the probed inventory
	// is available on its response.
	deferProbePastCommit bool
}

// virtualCandidateRotationContextKeyV3 carries the rotation intent across the
// detailed-resolver interface to the service that owns the candidate fallback.
type virtualCandidateRotationContextKeyV3 struct{}

func withVirtualCandidateRotationV3(ctx context.Context, allowed bool) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, virtualCandidateRotationContextKeyV3{}, allowed)
}

// WithVirtualCandidateRotation returns a context carrying the rotation intent
// for a detailed virtual resolve. It is the exported form of the internal
// setter, for callers outside this package that need to exercise intent
// threading (the core router's service adapter).
func WithVirtualCandidateRotation(ctx context.Context, allowed bool) context.Context {
	return withVirtualCandidateRotationV3(ctx, allowed)
}

// VirtualCandidateRotationAllowed reports whether the caller of a detailed
// virtual resolve asked to substitute a sibling for an excluded pinned
// candidate. Absent means allowed, so resolve paths that do not participate
// keep their pre-existing substitution behavior.
func VirtualCandidateRotationAllowed(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	allowed, ok := ctx.Value(virtualCandidateRotationContextKeyV3{}).(bool)
	if !ok {
		return true
	}
	return allowed
}

// virtualSessionBindingContextKeyV3 carries the session-binding intent across
// the detailed-resolver interface to the service that owns the profile refusal.
type virtualSessionBindingContextKeyV3 struct{}

func withVirtualSessionBindingV3(ctx context.Context, bound bool) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, virtualSessionBindingContextKeyV3{}, bound)
}

// WithVirtualSessionBinding returns a context carrying the session-binding
// intent for a detailed virtual resolve. It is the exported form of the
// internal setter, for callers outside this package (the core router's service
// adapter and tests).
func WithVirtualSessionBinding(ctx context.Context, bound bool) context.Context {
	return withVirtualSessionBindingV3(ctx, bound)
}

// VirtualSessionBinding reports whether the caller of a detailed virtual
// resolve is re-resolving a release an existing session is serving. Absent
// means session-bound (true): the conservative default, so a caller that does
// not participate still refuses a profile-removed substitution rather than
// silently swapping the release. Fresh-selection callers declare false
// explicitly.
func VirtualSessionBinding(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	bound, ok := ctx.Value(virtualSessionBindingContextKeyV3{}).(bool)
	if !ok {
		return true
	}
	return bound
}

// resetSubstitutedCandidateMetadata clears the declared media identity of the
// candidate the handler probed after the resolver served a different release
// (a fresh-selection fall-through). Without it the served transient would
// inherit the probed candidate's resolution, codecs and tracks, contradicting
// the resolved identity. The resolved candidate's catalog row or the forced
// probe then supplies the real metadata.
func resetSubstitutedCandidateMetadata(cand *VirtualPlaybackStream) {
	if cand == nil {
		return
	}
	cand.Resolution = ""
	cand.CodecVideo = ""
	cand.CodecAudio = ""
	cand.HDR = ""
	cand.SourceType = ""
	cand.FileSize = 0
	cand.Container = ""
	cand.Bitrate = 0
	cand.FrameRate = ""
	cand.HasAtmos = false
	cand.AudioLanguages = nil
	cand.SubtitleLanguages = nil
	cand.QualityScore = 0
}

// clearVirtualCandidateDeclaredMetadata clears the candidate-declared media
// fields on a transient file so a substituted resolve cannot serve the probed
// candidate's metadata under the resolved identity. Row identity and duration
// are preserved; the caller forces the probe so the resolved bytes supply the
// metadata.
func clearVirtualCandidateDeclaredMetadata(file *models.MediaFile) {
	if file == nil {
		return
	}
	file.Resolution = ""
	file.CodecVideo = ""
	file.CodecAudio = ""
	file.HDR = false
	file.Container = ""
	file.FileSize = 0
	file.Bitrate = 0
	file.AudioChannels = 0
	file.VideoTracks = nil
	file.AudioTracks = nil
	file.SubtitleTracks = nil
	file.ProbeSource = ""
	file.ProbeUpdatedAt = nil
}

// withResolvedCandidate stamps the resolver's actual result identity onto a
// resolved virtual source. The struct's Provider* fields and IdentityRematched
// are the resolver's own evidence about the candidate it returned, kept
// separate from the transient File identity, which is seeded from the requested
// row and therefore cannot by itself prove what was served.
func withResolvedCandidate(src *resolvedVirtualPlaybackSource, id virtualProbeIdentity, rematched bool) *resolvedVirtualPlaybackSource {
	if src == nil {
		return nil
	}
	src.ProviderVideoHash = id.VideoHash
	src.ProviderGUID = id.GUID
	src.ProviderReleaseName = id.ReleaseName
	src.ProviderReleaseSize = id.ReleaseSize
	src.IdentityRematched = rematched
	return src
}

// rehydratedMatchesPersistedIdentity reports whether a rehydration resolve is
// the same release as the session anchor row, judged by the durable identity
// tiers in the same precedence resolvedMatchesPersistedIdentity uses. It is the
// rehydration counterpart of the transport anchor's same-release assertion: a
// resolve with no usable identity tier can never confirm the release, so an
// unprovable rotation is refused instead of silently swapping releases.
func rehydratedMatchesPersistedIdentity(resolved resolvedVirtualPlaybackSource, row *models.MediaFile) bool {
	identity, ok := persistedVirtualIdentity(row)
	if !ok {
		return false
	}
	want := resolver.PersistedDedupKey(identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize)
	got := resolver.PersistedDedupKey(resolved.ProviderVideoHash, resolved.ProviderGUID, resolved.ProviderReleaseName, resolved.ProviderReleaseSize)
	return want != "" && want == got
}

// virtualRotationExclusionIdentity returns the durable provider identity the
// rotation-rejection memory is scoped by: the anchor row's own identity when it
// carries one, otherwise the zero identity. A zero identity means no chain —
// the rejection memory is per-row durable evidence and only meaningful while
// the row's identity is stable.
func virtualRotationExclusionIdentity(row *models.MediaFile) virtuallibrary.PersistedCandidateIdentity {
	identity, _ := persistedVirtualIdentity(row)
	return identity
}

// recordVirtualRotationRefusal durably remembers one rotated candidate that the
// same-release assertion rejected, so a later replan's rotation retry excludes
// it instead of replaying the identical substitution. The exclusion is keyed by
// the anchor's neutral source and the durable identity it was recorded under;
// the write is best-effort — a store failure degrades to the prior one-shot
// retry, not a resolve failure.
func (h *PlaybackHandler) recordVirtualRotationRefusal(ctx context.Context, row *models.MediaFile, candidateID string, identity virtuallibrary.PersistedCandidateIdentity) {
	h.recordVirtualRotationRefusalWithRecord(ctx, row, candidateID, identity, nil)
}

// recordVirtualRotationRefusalWithRecord is recordVirtualRotationRefusal with
// the caller's live attempt record threaded through. The plan store is keyed
// by session id, not file path, so callers that own the attempt (replan
// rehydration) pass it directly; other callers pass nil and fall back to a
// file-path lookup that misses. The revision compare-and-set runs against the
// committed chain so concurrent replans converge on the union.
func (h *PlaybackHandler) recordVirtualRotationRefusalWithRecord(ctx context.Context, row *models.MediaFile, candidateID string, identity virtuallibrary.PersistedCandidateIdentity, record *playback.AttemptRecordV3) {
	if h == nil || row == nil || candidateID == "" || identity == (virtuallibrary.PersistedCandidateIdentity{}) {
		return
	}
	store := h.recoveryStateStoreV3()
	if store == nil {
		return
	}
	var attempt *playback.AttemptRecordV3
	if record != nil {
		attempt = record
	} else {
		lookedUp, getErr := h.PlanStoreV3.GetAttempt(ctx, row.FilePath)
		if getErr != nil || lookedUp == nil {
			// Without an attempt row there is nowhere durable to record
			// the refusal, so degrade to the prior one-shot retry.
			return
		}
		attempt = lookedUp
	}
	releaseID := resolver.PersistedDedupKey(identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize)
	exclusion := playback.RecoveryExclusionV3{
		ProviderSource: virtualPlaybackNeutralKey(row.FilePath),
		CandidateID:    candidateID,
		ReleaseID:      releaseID,
		FileID:         row.ID,
	}
	// Reuse the bounded conflict-retry union so concurrent replans converge
	// instead of dropping all but the first refusal. On a caller-owned record
	// the merged chain is written back so the union read sees it.
	merged, appendErr := h.appendRecoveryExclusionsV3(ctx, attempt, []playback.RecoveryExclusionV3{exclusion})
	if appendErr != nil {
		slog.WarnContext(ctx, "virtual rotation refusal could not be persisted",
			"component", "api", "session_anchor", row.FilePath,
			"status", "rotation_refusal_unpersisted", "new_candidate_id", candidateID,
			"error", appendErr)
		return
	}
	attempt.RecoveryState = merged
}

// unionVirtualRotationExclusions merges the caller's transient exclusion list
// with the durable rotation-rejection chain recorded under the same anchor
// identity, so each replan retry excludes every candidate the same-release
// assertion already refused. The result is never written back to the attempt:
// the chain grows only through an actual refusal. A changed anchor identity
// yields the transient list unchanged.
func (h *PlaybackHandler) unionVirtualRotationExclusions(transient []string, row *models.MediaFile, identity virtuallibrary.PersistedCandidateIdentity) []string {
	return h.unionVirtualRotationExclusionsWithRecord(transient, row, identity, nil)
}

// unionVirtualRotationExclusionsWithRecord is unionVirtualRotationExclusions
// with the caller's live attempt record threaded through, so the chain is
// read from the record the replan owns instead of a file-path store lookup
// (the plan store is keyed by session id, not file path).
func (h *PlaybackHandler) unionVirtualRotationExclusionsWithRecord(transient []string, row *models.MediaFile, identity virtuallibrary.PersistedCandidateIdentity, record *playback.AttemptRecordV3) []string {
	if h == nil || row == nil || identity == (virtuallibrary.PersistedCandidateIdentity{}) {
		return transient
	}
	merged := append([]string(nil), transient...)
	for _, exclusion := range h.rotationRejectionChainWithRecord(row, identity, record) {
		if !containsStringExactV3(merged, exclusion) {
			merged = append(merged, exclusion)
		}
	}
	return merged
}

// rotationRejectionChainWithRecord returns the durable exclusion candidate
// ids recorded under the anchor's identity, or nil when no chain exists.
// The chain is read from the caller's live attempt record when one is
// threaded through (replan rehydration); a nil record falls back to a
// file-path store lookup for callers that own no attempt. The store remains
// the durable writer.
func (h *PlaybackHandler) rotationRejectionChainWithRecord(row *models.MediaFile, identity virtuallibrary.PersistedCandidateIdentity, record *playback.AttemptRecordV3) []string {
	if h == nil || h.PlanStoreV3 == nil {
		return nil
	}
	var attempt *playback.AttemptRecordV3
	if record != nil {
		attempt = record
	} else {
		var err error
		attempt, err = h.PlanStoreV3.GetAttempt(context.Background(), row.FilePath)
		if err != nil || attempt == nil {
			return nil
		}
	}
	source := virtualPlaybackNeutralKey(row.FilePath)
	releaseID := resolver.PersistedDedupKey(identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize)
	var chain []string
	for _, exclusion := range attempt.RecoveryState.Exclusions {
		// The chain is identity-gated: only exclusions recorded under the
		// anchor's CURRENT durable identity (and, when rows collide on a
		// candidate, the current row) union into the retry. A row whose
		// identity changed since a refusal was recorded yields nothing, so a
		// changed inventory invalidates the rejection memory instead of
		// suppressing a candidate that has since become the bound release.
		if exclusion.ProviderSource != source {
			continue
		}
		if releaseID == "" || exclusion.ReleaseID != releaseID {
			continue
		}
		if row.ID > 0 && exclusion.FileID > 0 && exclusion.FileID != row.ID {
			continue
		}
		chain = append(chain, exclusion.CandidateID)
	}
	return chain
}

// resolveRehydratedVirtualSourceV3 resolves the session-bound virtual source for
// a replan rehydration. When the pinned candidate is absent from the provider's
// current list the resolver refuses with ErrSessionBoundCandidateAbsent; this
// retries once with candidate rotation declared and the relist forced, so a
// renumbered or dropped anchor rotates to a live sibling under the same session
// binding. The retry is narrowly scoped to that cause: a generic provider or
// resolve failure is returned unchanged, and a caller that already declared
// rotation is never retried.
//
// A refusal of a rotated candidate is persisted into the attempt's recovery
// exclusion chain (see recordVirtualRotationRefusal), keyed by the anchor's
// neutral source and durable identity. The next replan's retry unions that
// chain into its exclusion list, so each incompatible candidate is tried at
// most once under the current inventory: the retry either finds a fresh
// candidate, exhausts the listing and keeps the original absent cause, or hits
// the live-candidate attempt bound without replaying an already-refused one.
// A changed anchor identity yields no chain, so a changed inventory
// invalidates the rejection memory rather than suppressing a candidate that
// has since become the bound release.
func (h *PlaybackHandler) resolveRehydratedVirtualSourceV3(
	r *http.Request,
	pinnedFile *models.MediaFile,
	profileID string,
	excludedCandidateIDs []string,
	preferredCandidateID string,
	qualityPreference string,
	bandwidthCapKbps int,
	opts virtualResolveOptionsV3,
) (resolvedVirtualPlaybackSource, error) {
	return h.resolveRehydratedVirtualSourceWithAttemptV3(r, pinnedFile, profileID, excludedCandidateIDs, preferredCandidateID, qualityPreference, bandwidthCapKbps, opts, nil)
}

// resolveRehydratedVirtualSourceWithAttemptV3 is resolveRehydratedVirtualSourceV3
// with the caller's live attempt record threaded through, so a rotation refusal
// is recorded on the attempt the replan owns instead of re-looking it up by
// file path (the plan store is keyed by session id, not file path). A nil
// record degrades to the file-path lookup for callers that own no attempt.
func (h *PlaybackHandler) resolveRehydratedVirtualSourceWithAttemptV3(
	r *http.Request,
	pinnedFile *models.MediaFile,
	profileID string,
	excludedCandidateIDs []string,
	preferredCandidateID string,
	qualityPreference string,
	bandwidthCapKbps int,
	opts virtualResolveOptionsV3,
	record *playback.AttemptRecordV3,
) (resolvedVirtualPlaybackSource, error) {
	// The session anchor file is the row the plan was built against, but the
	// session can bind a concrete ?result= candidate whose own catalog row is
	// the one carrying the durable identity. Capture that identity up front so
	// the rotation-exclusion chain, the retry context and the same-release
	// assertion below all judge the release the session is actually serving
	// rather than an identity-less placeholder.
	pinnedFile = h.enrichSessionBoundCandidateIdentity(r.Context(), pinnedFile)
	chainIdentity := virtualRotationExclusionIdentity(pinnedFile)
	excludedCandidateIDs = h.unionVirtualRotationExclusionsWithRecord(excludedCandidateIDs, pinnedFile, chainIdentity, record)
	resolved, err := h.resolveVirtualPlaybackSource(r, pinnedFile, profileID, false, excludedCandidateIDs, preferredCandidateID, qualityPreference, bandwidthCapKbps, false, opts)
	if err == nil || opts.rotateCandidates || !isRehydratedVirtualSourceRotatableV3(err) {
		return resolved, err
	}
	if len(excludedCandidateIDs) == 0 {
		if id := virtualResultCandidateID(pinnedFile.FilePath); id != "" {
			excludedCandidateIDs = []string{id}
		}
	}
	rotatedOpts := opts
	rotatedOpts.rotateCandidates = true
	// Thread the anchor's durable identity so the resolver can re-identify a
	// renumbered same-release candidate rather than mistake it for a sibling.
	// A row with no durable identity is unchanged and the assertion below then
	// refuses any rotation it cannot prove is the same release, exactly as the
	// transport anchor does. The identity is captured from the row that owns the
	// session's concrete candidate when the anchor row in hand is the
	// provider-neutral placeholder, so the retry and the assertion below judge
	// the same release the session is actually serving.
	retryReq := r.WithContext(virtualResolveContextWithPersistedIdentity(r.Context(), pinnedFile))
	rotated, rotateErr := h.resolveVirtualPlaybackSource(retryReq, pinnedFile, profileID, false, excludedCandidateIDs, preferredCandidateID, qualityPreference, bandwidthCapKbps, true, rotatedOpts)
	if rotateErr != nil {
		slog.WarnContext(r.Context(), "virtual replan candidate rotation failed",
			"component", "api", "session_anchor", pinnedFile.FilePath,
			"status", "rotation_failed", "old_candidate_id", virtualResultCandidateID(pinnedFile.FilePath),
			"error", logredact.SanitizeURLError(rotateErr))
		// The retry declared rotation, so a failure here is the rotation's own
		// exhaustion verdict rather than a verdict about one listing token.
		// Mark it so a replan does not repeat this resolve for every sibling
		// edition before reaching the same answer.
		return rotated, fmt.Errorf("%w: %w", errVirtualRotationExhaustedV3, rotateErr)
	}
	// Same-release assertion, mirroring the transport anchor refusal. A
	// rotation that resolves a genuinely different release must not anchor the
	// rehydrated plan on sibling bytes; return the original absent/marked-failed
	// cause so the caller keeps its terminal/rotation policy.
	if !rotated.IdentityRematched && !rehydratedMatchesPersistedIdentity(rotated, pinnedFile) {
		slog.WarnContext(r.Context(), "virtual replan candidate rotation resolved a different release; refusing a silent release swap",
			"component", "api", "session_anchor", pinnedFile.FilePath,
			"status", "rotation_refused", "old_candidate_id", virtualResultCandidateID(pinnedFile.FilePath),
			"new_candidate_id", virtualResultCandidateID(rotated.URI))
		h.recordVirtualRotationRefusalWithRecord(r.Context(), pinnedFile, virtualResultCandidateID(rotated.URI), chainIdentity, record)
		return resolved, err
	}
	slog.InfoContext(r.Context(), "virtual replan rotated an absent session-bound candidate",
		"component", "api", "session_anchor", pinnedFile.FilePath,
		"status", "rotated", "old_candidate_id", virtualResultCandidateID(pinnedFile.FilePath),
		"new_candidate_id", virtualResultCandidateID(rotated.URI), "virtual_uri", rotated.URI)
	return rotated, nil
}

// resolveVirtualAnchorURIWithRotationV3 resolves the session-bound virtual
// anchor for transport preparation (the remux seek anchor and the transport
// input). The retry gate is the same provider-listing outage predicate the
// plan-time walk and the serve layer use, so an empty provider listing (a
// transient blackout, not an absent pin) also gets a forced relist instead of
// terminaling the already-built plan at the first live resolve.
//
// The retry distinguishes a release verdict from a transient listing outage:
//
//   - A verdict that indicts the release — an absent session pin
//     (ErrSessionBoundCandidateAbsent) or a catalog marked-failed candidate —
//     excludes the dead pin and declares rotation, so a renumbered same-release
//     candidate is re-identified rather than mistaken for a sibling. The anchor
//     row's durable identity is threaded so the re-match can prove the release.
//   - A transient listing outage (an empty answer, a 5xx, or a trusted pin
//     absent from an empty answer) is not a verdict. The retry re-lists and
//     re-serves the SAME bound pin without excluding or indicting it, so a
//     provider that recovers with the same candidate id recovers instead of
//     having the healthy candidate skipped as if it were dead.
//
// The forced retry is marked as a provider-outage relist. Without that marker a
// forced resolve re-honors the fresh-serve floor and the provider-failure
// backoff, so it would replay the very empty answer (or fail-fast negative) the
// outage just cached; the marker bypasses both. This matters for identity-less
// rows too, which the evidence-gated outage retry inside resolveVirtualInputURI
// cannot cover.
//
// It accepts a substituted candidate only when it is the same release as the
// anchor: the resolver reports IdentityRematched, or the candidate's durable
// identity tier matches the row's. Serving the pinned id itself is accepted
// unconditionally — it is the bound candidate. A genuinely different release is
// refused with the original cause instead of silently anchoring the already-
// built plan on sibling bytes; the caller keeps its terminal/rotation policy and
// no release swap happens under a plan that never replanned. This is the same
// narrow contract the serve layer (stream.go) and the replan rehydration
// (resolveRehydratedVirtualSourceV3) apply.
func (h *PlaybackHandler) resolveVirtualAnchorURIWithRotationV3(
	ctx context.Context,
	session *playback.Session,
	file *models.MediaFile,
) (ResolvedVirtualMedia, func(), error) {
	// Scope note: this path owns no attempt record, so it neither reads nor
	// persists the rotation-rejection chain — the lookup below and the
	// refusal record both degrade to the prior one-shot retry. The durable
	// chain is a replan-rehydration mechanism; transport-anchor churn stays
	// at one rotation attempt per call, and a later replan records its own
	// refusal when it retries.
	chainIdentity := virtualRotationExclusionIdentity(file)
	excluded := h.unionVirtualRotationExclusions(nil, file, chainIdentity)
	resolved, cleanup, err := h.resolveVirtualInputURI(
		ctx, file.FilePath, file.VirtualOwnerInstallationID,
		session.UserID, session.ProfileID, false, excluded, "",
	)
	if err == nil || (!virtualProviderListingOutage(err) && !errors.Is(err, ErrVirtualCandidateMarkedFailed)) {
		return resolved, cleanup, err
	}
	pinnedID := virtualResultCandidateID(file.FilePath)
	// Only a verdict about the release authorizes excluding (and rotating past)
	// the bound pin. A transient listing outage must retry the pin it still
	// believes in; excluding it would skip the healthy candidate the moment the
	// provider recovers with the same id.
	releaseIndicted := errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) ||
		errors.Is(err, ErrVirtualCandidateMarkedFailed)
	if releaseIndicted && pinnedID != "" && !containsStringExactV3(excluded, pinnedID) {
		excluded = append(excluded, pinnedID)
	}
	// Thread the durable identity explicitly: the retry relists (forceRefresh),
	// so the stored-row lookup that normally carries the identity is bypassed. A
	// legacy row with no identity is unchanged; a same-release re-match then
	// cannot be proven and the assertion below keeps the original cause.
	retryCtx := virtualResolveContextWithPersistedIdentity(ctx, file)
	// Mark the forced retry as an outage relist so the resolver asks the
	// provider again instead of serving the recent empty answer or honoring the
	// provider-failure backoff the outage just recorded.
	retryCtx = virtuallibrary.WithProviderOutageRelist(retryCtx)
	rotated, rotatedCleanup, rotateErr := h.resolveVirtualInputURI(
		retryCtx, file.FilePath, file.VirtualOwnerInstallationID,
		session.UserID, session.ProfileID, true, excluded, "", releaseIndicted,
	)
	if rotateErr != nil {
		return rotated, rotatedCleanup, rotateErr
	}
	// The bound pin itself is never a substitution, even without durable
	// identity: a provider that recovered with the same candidate id resolves
	// exactly the release the session is serving.
	samePin := pinnedID != "" && rotated.CandidateID == pinnedID
	if !samePin && !rotated.IdentityRematched && !resolvedMatchesPersistedIdentity(rotated, file) {
		if rotatedCleanup != nil {
			rotatedCleanup()
		}
		slog.WarnContext(ctx, "virtual transport anchor rotation resolved a different release; refusing a silent anchor swap",
			"component", "api", "session_anchor", file.FilePath,
			"status", "rotation_refused", "old_candidate_id", pinnedID,
			"new_candidate_id", virtualResultCandidateID(rotated.URI))
		h.recordVirtualRotationRefusal(ctx, file, virtualResultCandidateID(rotated.URI), chainIdentity)
		return ResolvedVirtualMedia{}, nil, err
	}
	slog.InfoContext(ctx, "virtual transport anchor re-resolved the session-bound candidate",
		"component", "api", "session_anchor", file.FilePath,
		"status", "recovered", "old_candidate_id", pinnedID,
		"new_candidate_id", virtualResultCandidateID(rotated.URI), "virtual_uri", rotated.URI,
		"release_indicted", releaseIndicted)
	return rotated, rotatedCleanup, nil
}

// resolveVirtualAnchorURIExcludingFailedV3 re-resolves the session-bound
// candidate while excluding one failed result id, so a seek-anchor retry
// whose relay token 5xxes can walk to an alternate candidate instead of
// re-probing the token that just failed. Only a same-identity candidate is
// accepted: like resolveVirtualAnchorURIWithRotationV3 it refuses a silent
// swap to a different release, so the anchored bytes stay the planned
// release under a new result id.
//
// Scope note: this path owns no attempt record, so it neither reads nor
// persists the rotation-rejection chain — the union below and the refusal
// record both degrade to the prior one-shot retry. The durable chain is a
// replan-rehydration mechanism; a refusal here keeps the original cause and
// a later replan records its own refusal when it retries.
func (h *PlaybackHandler) resolveVirtualAnchorURIExcludingFailedV3(
	ctx context.Context,
	session *playback.Session,
	file *models.MediaFile,
	excludeResultID string,
) (ResolvedVirtualMedia, func(), error) {
	if strings.TrimSpace(excludeResultID) == "" {
		return ResolvedVirtualMedia{}, nil, errors.New("virtual anchor rotation needs a failed candidate to exclude")
	}
	// Thread the durable identity explicitly, mirroring the absent-pin
	// rotation: the retry relists, so the stored-row lookup that normally
	// carries the identity is bypassed.
	chainIdentity := virtualRotationExclusionIdentity(file)
	excluded := h.unionVirtualRotationExclusions([]string{excludeResultID}, file, chainIdentity)
	retryCtx := virtualResolveContextWithPersistedIdentity(ctx, file)
	rotated, rotatedCleanup, rotateErr := h.resolveVirtualInputURI(
		retryCtx, file.FilePath, file.VirtualOwnerInstallationID,
		session.UserID, session.ProfileID, true, excluded, "", true,
	)
	if rotateErr != nil {
		return rotated, rotatedCleanup, rotateErr
	}
	if !rotated.IdentityRematched && !resolvedMatchesPersistedIdentity(rotated, file) {
		if rotatedCleanup != nil {
			rotatedCleanup()
		}
		slog.WarnContext(ctx, "virtual transport anchor rotation resolved a different release; refusing a silent anchor swap",
			"component", "api", "session_anchor", file.FilePath,
			"status", "rotation_refused", "excluded_candidate_id", excludeResultID,
			"new_candidate_id", virtualResultCandidateID(rotated.URI))
		h.recordVirtualRotationRefusal(ctx, file, virtualResultCandidateID(rotated.URI), chainIdentity)
		return ResolvedVirtualMedia{}, nil, errors.New("virtual anchor rotation found no same-identity alternate")
	}
	slog.InfoContext(ctx, "virtual transport anchor rotated past a failed candidate",
		"component", "api", "session_anchor", file.FilePath,
		"status", "rotated", "excluded_candidate_id", excludeResultID,
		"new_candidate_id", virtualResultCandidateID(rotated.URI), "virtual_uri", rotated.URI)
	return rotated, rotatedCleanup, nil
}

func (h *PlaybackHandler) resolveVirtualPlaybackSource(r *http.Request, file *models.MediaFile, profileID string, deferProbe bool, excludedCandidateIDs []string, preferredCandidateID string, qualityPreference string, bandwidthCapKbps int, forceRelist bool, opts ...virtualResolveOptionsV3) (resolvedVirtualPlaybackSource, error) {
	options := virtualResolveOptionsV3{}
	if len(opts) > 0 {
		options = opts[0]
	}
	allowFailed := options.allowFailedCandidate
	rotateCandidates := options.rotateCandidates
	explicitSelection := options.explicitSelection
	if !isVirtualPlaybackFile(file) {
		return resolvedVirtualPlaybackSource{File: file}, nil
	}
	if h.VirtualPlaybackResolver == nil {
		return resolvedVirtualPlaybackSource{}, errors.New("virtual playback resolver is not configured")
	}
	// A pinned resolve (a session binding or an explicit version pick) must
	// thread the durable identity of the candidate it is bound to, but the row
	// in hand can be the provider-neutral placeholder the session was requested
	// through while the session anchors a concrete ?result= candidate. Overlay
	// that owning row's identity and stored URL so a provider re-list that
	// renumbers the same release is re-matched instead of refused as an
	// identity-less dead pin. Only pinned resolves opt in, mirroring the
	// identity threading on the resolver call below; an auto selection is left
	// untouched and keeps its fallback.
	if options.sessionBound || options.explicitSelection {
		file = h.enrichSessionBoundCandidateIdentity(r.Context(), file)
	}
	// A recovery bypass re-lists the provider past its failure floor. Bound it
	// with the same per-provider damper the stale fallback uses, so repeated
	// starts cannot keep a dead provider hot: once this (content, owner)
	// listing's budget is spent the resolve honors the provider floor instead
	// of bypassing it. A listing or resolve that answers clears the budget on
	// the way out, so a recovered provider starts fresh.
	bypassProviderFloor := options.bypassProviderFloor
	if bypassProviderFloor {
		recoveryKey := virtualRecoveryRelistKey(virtualPlaybackNeutralKey(file.FilePath), file.VirtualOwnerInstallationID)
		if !virtualRecoveryRelists.allow(recoveryKey) {
			slog.WarnContext(r.Context(), "virtual recovery re-list budget exhausted; honoring the provider floor",
				"component", "api", "file_id", file.ID, "neutral_key", virtualPlaybackNeutralKey(file.FilePath))
			bypassProviderFloor = false
		}
	}
	// providerRelist drives both the declared-outage cache bypass and the
	// resolver's forced re-list. It must be true whenever a fresh provider
	// listing is required past the floor: a forced relist or any declared
	// recovery (see the attempt-context comment below).
	providerRelist := forceRelist || rotateCandidates || bypassProviderFloor
	// Capture one write generation for this resolve before any work starts.
	// Every cache entry and sticky pin this resolve writes carries it, so a
	// resolve that finishes late cannot overwrite a newer resolve's evidence.
	generation := nextVirtualCacheGeneration()
	// Split file_load_probe into its provider phases so a cold-start
	// attribution is measured, not guessed. The deferred log runs on every
	// return below, including the fast paths.
	trace := &virtualResolveTrace{started: time.Now(), budgetMS: virtualStartupBudget.Milliseconds()}
	defer trace.log(r.Context(), file)
	// One deadline owns the entire cold path below. Listing,
	// provider resolution, probing, retries and the stale-source fallback all
	// derive from this context, so an early stage that burns time cannot hand a
	// later stage a fresh virtualStartupBudget. Detached background work
	// deliberately re-bases off r.Context() and is not bounded by this.
	coldCtx, coldCancel := context.WithTimeout(r.Context(), virtualStartupBudget)
	defer coldCancel()
	// Attribute provider discovery to its own stage: the observer fires once per
	// provider listing this cold path initiates, so the enclosing list/resolve
	// stages can report their wall time net of discovery and a repeat start can
	// show fetch_calls=0 instead of hiding the cost under a probe label.
	coldCtx, fetchSnapshot := observeVirtualProviderFetches(coldCtx, virtualProviderLinkLatencyKey(file))
	// Candidate selection (listing) must not be able to
	// consume the resolve/probe attempt's budget. It runs under a staging
	// deadline that stops at half the cold budget, reserving the other half for
	// the attempt loop below. The attempt still inherits coldCtx, so the single
	// overall cap and the no-restart property are unchanged; the staging bound
	// only guarantees that a slow early stage cannot starve a later one.
	stagingCtx, stagingCancel := context.WithTimeout(coldCtx, virtualStartupBudget/2)
	defer stagingCancel()
	userID := apimw.GetUserID(r.Context())
	parsed, _ := url.Parse(file.FilePath)
	candidates := []VirtualPlaybackStream{{
		URI: file.FilePath, OwnerInstallationID: file.VirtualOwnerInstallationID,
		Resolution: file.Resolution, CodecVideo: file.CodecVideo, CodecAudio: file.CodecAudio,
		HDR: mediaFileHDRString(file),
	}}
	noResult := parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) == ""
	// persistedResultURI is true when the catalog row already points at an
	// adopted provider-neutral candidate rather than the neutral virtual path.
	// It is computed here because the durable-resume derivation below needs it
	// before the listing gate.
	persistedResultURI := parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) != ""
	// A stale row whose stored evidence cannot be trusted must be probed even
	// when it looks complete on its cached metadata; the probe gate makes this
	// decision on its own so this declaration does not need that input.
	needsProbeVerification := file == nil || file.ProbeUpdatedAt == nil
	needsCandidateMetadata := (!noResult && !virtualFileHasPlayableDeclaredMetadata(file)) ||
		(noResult && (!completeVirtualVideoEvidenceV3(file) || !completeVirtualAudioEvidenceV3(file) || !completeVirtualContainerEvidenceV3(file)))
	// A concrete ?result= candidate with sufficient declared metadata
	// already names the candidate to serve: it does not need the provider
	// list. Probe verification (real inventory) arrives via the declared
	// fast path below, not via the listing gate.
	deviceCaps, hasCaps := h.requestDeviceCapabilities(r)
	fingerprint := ""
	if hasCaps {
		fingerprint = deviceCaps.Fingerprint()
	}
	stickyKey := bestResultCacheKey(file.ContentID, virtualPlaybackNeutralKey(file.FilePath), file.VirtualOwnerInstallationID, fingerprint)
	pinnedURI := h.peekVirtualSticky(stickyKey)
	// Durable-resume derivation. The sticky pin and best-result cache are
	// process state and a restart loses both, but the requested row is durable:
	// when it already owns a concrete ?result= candidate whose persisted URL is
	// still usable, the row itself says what to serve. Re-derive the pin from
	// the row and remember the candidate so the deferred fast path below can
	// behave exactly as a warm pin/cache hit would instead of paying a fresh
	// provider listing.
	//
	// Gated on the repeat-play fast path being unavailable for this row:
	// complete probed evidence plus a probe stamp takes that path already, so
	// evaluating the stored URL (which re-validates it, including DNS) would
	// only add latency. The verdict and exclusion gates match the fast paths
	// below, so a row this derivation skips still resolves as before.
	persistedResumeURI := ""
	var persistedResumeStored ResolvedVirtualMedia
	persistedResumeState := virtualStoredURLMissing
	if persistedResultURI && needsProbeVerification &&
		len(excludedCandidateIDs) == 0 &&
		(allowFailed || !virtualCandidateVerdictActive(file.FailedAt, time.Now())) {
		// A usable stored URL always keeps the persisted candidate. An
		// expired-but-trusted one does too, except under an explicit relink:
		// then the relist is the only way to refresh a lapsed signed URL, so it
		// proceeds. The expired URL itself is never served.
		stored, storedState := evaluateStoredVirtualURLCandidate(
			stagingCtx, file.FilePath, file,
			h.storedVirtualURLAllowInsecure(file, file.VirtualOwnerInstallationID),
			time.Now(), h.virtualCandidateTrustWindow(),
		)
		persistedResumeState = storedState
		trustedResume := storedState == virtualStoredURLExpiredWithinWindow && !forceRelist
		if storedState == virtualStoredURLUsable || trustedResume {
			persistedResumeStored = stored
			persistedResumeURI = file.FilePath
			h.pinVirtualStickyAt(stickyKey, persistedResumeURI, generation)
			pinnedURI = persistedResumeURI
			if trustedResume {
				slog.InfoContext(r.Context(), "virtual durable resume: keeping the persisted candidate inside the trust window",
					"component", "api", "file_id", file.ID, "candidate_uri", file.FilePath,
					"status", "trusted_resume", "reason", "stored_url_expired_within_window")
			}
		}
	}
	// Check the best-result cache before listing candidates. A previous
	// successful play of this content may have a cached result= URI that
	// lets us skip the entire list+resolve+probe sequence on replay.
	cachedListing := false
	trace.cacheMissCause = "none" //nolint:goconst // trace cause vocabulary, kept inline for readability.
	if noResult {
		if h.BestResultCache == nil {
			trace.cacheMissCause = "cache_disabled" //nolint:goconst // trace cause vocabulary, kept inline for readability.
		} else {
			neutralURI := virtualPlaybackNeutralKey(file.FilePath)
			cacheKey := bestResultCacheKey(file.ContentID, neutralURI, file.VirtualOwnerInstallationID, fingerprint)
			cached, cause := h.BestResultCache.getWithCause(cacheKey, time.Now())
			if len(cached) == 0 && fingerprint != "" {
				// A listing warmed without a device fingerprint (the bounded
				// prefetch path) is still the filtered, device-neutral candidate
				// set; only the ranking below is device-specific, so fall back to
				// it rather than paying the provider list again on this device's
				// first click.
				cached, cause = h.neutralFallbackCacheCause(cached, cause, file, neutralURI)
			}
			if len(cached) == 0 && cause != "" {
				trace.cacheMissCause = cause
			}
			if len(cached) > 0 {
				// Cache holds the filtered, device-neutral candidate list; rank it
				// for this device so a TV and a phone pick their own best stream
				// without another provider round-trip, then keep rejected streams
				// behind accepted ones.
				candidates = h.finalizeVirtualCandidateOrder(r, cached, qualityPreference, bandwidthCapKbps)
				noResult = false // treated as if file already had a result=
				cachedListing = len(candidates) > 0
				trace.cached = cachedListing
				trace.cacheMissCause = "none" //nolint:goconst // trace cause vocabulary, kept inline for readability.
			}
		}
	} else {
		trace.cacheMissCause = "explicit" //nolint:goconst // trace cause vocabulary, kept inline for readability.
	}
	// A valid cached listing already carries the candidate metadata the list
	// call exists to fetch, so it does not need the provider round-trip even
	// when the stored row has no probe evidence yet. Only a cold cache or an
	// explicit relist forces the list.
	//
	// A failed requested row or a pending exclusion additionally requires the
	// provider list even when the row has complete probe evidence: the fast
	// paths are gated off for those rows, so without a list the candidate set
	// would contain only the rejected row and rotation could never find a
	// sibling.
	exclusionPending := len(excludedCandidateIDs) > 0
	requestedRowUnusable := !allowFailed && virtualCandidateVerdictActive(file.FailedAt, time.Now())
	// A durable-resume hit suppresses the list the same way a warm best-result
	// cache does: the row already names the candidate to serve, so there is no
	// candidate metadata left to fetch. It is only set for a concrete same-row
	// candidate that carries a usable stored URL, so it cannot mask an
	// exclusion, a forced relist, a neutral row, or a failed verdict.
	emptyListKey := virtualRecoveryRelistKey(virtualPlaybackNeutralKey(file.FilePath), file.VirtualOwnerInstallationID)
	// A provider that just answered empty stays suppressed for the window on
	// the start path: recovery, forced relists, exclusions, and unusable-row
	// rotations still list, so failover is never blocked. Suppression only
	// skips a repeat cold list that would pay the full ~3.5s again.
	emptySuppressed := !forceRelist && !exclusionPending && !requestedRowUnusable && virtualEmptyListSuppressed(emptyListKey, time.Now())
	if emptySuppressed {
		slog.DebugContext(r.Context(), "virtual playback list suppressed: provider answered empty inside window",
			"component", "api", "content_id", file.ContentID, "file_id", file.ID)
	}
	// An explicit concrete candidate with sufficient declared metadata names
	// the candidate to serve; skipping the provider list is what makes the
	// declared fast path below instant.
	declaredCandidateComplete := !noResult && virtualFileHasPlayableDeclaredMetadata(file)
	// listReason is observational only: it names why the gate below decided
	// the way it did so a day of timing lines can size the convertible share
	// (starts that listed but could have reused a fresh cache) without
	// guessing. Unknown stays unknown rather than a fabricated miss cause.
	//
	// The pre-list switch records intent (fast-path eligible, cache reused,
	// resumed, declared, suppressed); the listing gate below overwrites it
	// with "listed" when the provider round-trip actually runs. A later fast
	// path that skips the provider without an early cache/resume/declared
	// hit (durable-resume inside the attempt loop, delivery grace) keeps the
	// pre-list "unknown": the outcome is fast (fast_path=true, listed=false)
	// even though no early gate predicted it.
	switch {
	case fastPathGateEligible(deferProbe, forceRelist, noResult, excludedCandidateIDs):
		trace.listReason = "fast_path"
	case cachedListing:
		trace.listReason = "cache_hit"
	case persistedResumeURI != "":
		trace.listReason = "resumed"
	case declaredCandidateComplete:
		trace.listReason = "explicit_declared"
	case emptySuppressed:
		trace.listReason = "empty_suppressed"
	default:
		trace.listReason = "unknown"
	}
	if (shouldListVirtualPlaybackCandidates(noResult, needsCandidateMetadata && !cachedListing && !declaredCandidateComplete, forceRelist) ||
		((exclusionPending || requestedRowUnusable) && !cachedListing)) && persistedResumeURI == "" && !emptySuppressed && h.VirtualPlaybackStreamLister != nil {
		trace.listed = true
		trace.listReason = "listed"
		trace.listRan = true
		// The gate listed, so name why the cache did not serve. A miss cause
		// already recorded above stands; otherwise attribute the bypass: an
		// explicit relist honored the caller's force flag, an
		// exclusion/unusable rotation had to refresh past the cache, and an
		// explicit ?result= row never consults it. Unknown stays unknown.
		if trace.cacheMissCause == "none" || trace.cacheMissCause == "" {
			switch {
			case forceRelist:
				trace.cacheMissCause = "forced_relist"
			case exclusionPending || requestedRowUnusable:
				trace.cacheMissCause = "exclusion_or_unusable"
			case !noResult:
				trace.cacheMissCause = "explicit"
			default:
				trace.cacheMissCause = "unknown"
			}
		}
		fetchBeforeDur, fetchBeforeCalls := fetchSnapshot()
		listStart := time.Now()
		// Candidate listing is part of the startup critical path. Keep it
		// bounded so the first-byte SLA cannot be defeated before resolution,
		// but derive that bound from the single cold-path deadline so listing
		// cannot restart the budget. The 15s cap is a backstop for a staging
		// budget larger than that; the staging deadline always wins when it
		// is smaller.
		listCtx, cancel := context.WithTimeout(stagingCtx, 15*time.Second)
		listWait := startVirtualResolveWaitLog(stagingCtx, "fetch", "provider_listing", file.ContentID, file.ID, file.FilePath,
			virtualProbeBudget, virtualProbeBudget)
		streams, err := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(
			listCtx, file.FilePath, userID, profileID, file.VirtualOwnerInstallationID,
		)
		listWait.finish(err)
		listDiscovery, listCalls := fetchSnapshot()
		listDiscovery -= fetchBeforeDur
		listCalls -= fetchBeforeCalls
		listAnswered := err == nil && len(streams) > 0
		if listAnswered {
			virtualEmptyListClear(emptyListKey)
		} else if err == nil {
			// An answered empty list suppresses a hot re-list. A listing
			// error — especially a client cancellation — is not provider
			// evidence and must not suppress the next viewer's start.
			virtualEmptyListRecord(emptyListKey, time.Now())
		}
		if err == nil && len(streams) > 0 {
			if len(streams) > maxVirtualPlaybackStreams {
				streams = streams[:maxVirtualPlaybackStreams]
			}
			// The provider answered with candidates, so a recovery bypass is no
			// longer defeating a provider fail-fast: clear this listing's
			// budget so a later failure starts from a full window. An empty
			// answer deliberately does not clear, so a provider that keeps
			// answering [] still accumulates toward the bound.
			virtualRecoveryRelists.clear(virtualRecoveryRelistKey(virtualPlaybackNeutralKey(file.FilePath), file.VirtualOwnerInstallationID))
			// A selected result= URI is still an active catalog row referenced by
			// the playback attempt: its metadata is refreshed in memory. The
			// just-listed candidate set is persisted separately, in the
			// background, so a later open of this title sees the real version
			// list instead of the seeded placeholder.
			if noResult && h.VirtualPlaybackStreamSink != nil {
				h.spawnVirtualCandidateSink(listCtx, file, visibleVirtualPlaybackStreams(streams))
			}
			filtered := filterVirtualPlaybackStreams(file, streams)
			if h.BestResultCache != nil && len(filtered) > 0 {
				neutralURI := virtualPlaybackNeutralKey(file.FilePath)
				cacheKey := bestResultCacheKey(file.ContentID, neutralURI, file.VirtualOwnerInstallationID, fingerprint)
				h.BestResultCache.setWithDetailsAt(cacheKey, file.ContentID, neutralURI, file.VirtualOwnerInstallationID, filtered, time.Now(), generation)
			}
			if noResult {
				if len(filtered) > 0 {
					candidates = h.finalizeVirtualCandidateOrder(r, filtered, qualityPreference, bandwidthCapKbps)
				}
			} else {
				// Explicit candidate selected. Find it in streams to enrich its metadata,
				// and keep it at index 0 without overwriting it with device re-ranking.
				resultID := ""
				if parsed != nil {
					resultID = strings.TrimSpace(parsed.Query().Get("result"))
				}
				pinFound := false
				for _, s := range streams {
					if s.URI == file.FilePath || (resultID != "" && s.ID == resultID) {
						candidates[0] = s
						pinFound = true
						break
					}
				}
				if len(filtered) > 0 {
					rankedAlternatives := h.finalizeVirtualCandidateOrder(r, filtered, qualityPreference, bandwidthCapKbps)
					now := time.Now()
					// Inside the trust window, keep the persisted same-identity
					// candidate ahead of the fresh list even when the relist
					// dropped it. The exception is a lapsed signed URL: only a
					// relist can refresh it, so the stale pin is dropped as
					// before. Beyond the window, or for a legacy row with no
					// durable identity, behavior is unchanged.
					keepPersisted := h.persistedVirtualCandidateTrusted(file, now) &&
						!virtualStoredURLNeedsSignedRefresh(file, now)
					if forceRelist && !pinFound && !keepPersisted {
						// The forced fresh listing no longer carries the pinned
						// version. Drop the stale pin instead of retrying a
						// candidate the provider stopped listing; the ranked
						// fresh list takes over and rotation/stale-fallback can
						// work from it.
						candidates = rankedAlternatives
					} else {
						candidates = append([]VirtualPlaybackStream{candidates[0]}, rankedAlternatives...)
					}
				}
			}
		}
		cancel()
		// Report the listing as its wall time net of the provider discovery it
		// paid, and move that discovery into the fetch stage. A listing served
		// entirely from the resolver cache contributes nothing here.
		trace.list = virtualStageNetElapsed(listStart, listDiscovery)
		if listDiscovery > 0 {
			trace.fetch += listDiscovery
			trace.fetchRan = true
		}
		if listCalls > 0 {
			trace.fetchCalls += listCalls
		}
	}
	maxAttempts := h.maxVirtualFailoverAttempts(r.Context())
	if noResult {
		candidates = h.applyVirtualStickyPin(stickyKey, pinnedURI, candidates, deviceCaps)
	}
	// Prefer planner-grade probed evidence on a fresh (unbound, non-explicit)
	// start: a candidate whose catalog row already carries complete video,
	// audio and container evidence (plus a probe stamp) is a known-good
	// release, while an unprobed stub is speculative. This runs before the
	// candidate cap so the probed row survives maxAttempts, and inside each
	// accepted/rejected group so compatibility still wins — evidence only
	// breaks ties within a group. Explicit picks and session-bound resolves
	// are untouched: the viewer chose that exact release.
	//
	// The preference also runs when the requested row is itself an unprobed
	// placeholder that already carries a persisted ?result= (noResult false):
	// otherwise the request binds the size-0 stub at index 0 while a fully
	// probed row for the same profile/content exists, and the session later
	// has to rotate — the very cascade this preference exists to avoid. A row
	// the planner can route (complete evidence) keeps the existing noResult-
	// only behavior, so this only ever promotes a probed sibling over a stub.
	if (noResult || needsProbeVerification) && !options.sessionBound && !options.explicitSelection && len(candidates) > 1 {
		candidates = h.preferProbedVirtualCandidates(stagingCtx, candidates, file, file.VirtualOwnerInstallationID)
	}
	if len(candidates) > maxAttempts {
		candidates = candidates[:maxAttempts]
	}
	trace.candidates = len(candidates)
	// The attempt loop, its probes, its retries and the stale-source fallback
	// run on the same cold-path deadline. Reusing coldCtx directly means they
	// observe whatever budget listing left, rather than a
	// freshly restarted virtualStartupBudget. The staging deadline above caps
	// those early stages at half the cold budget, so the attempt is guaranteed
	// at least that much time rather than being starved.
	attemptCtx := coldCtx
	// A declared recovery re-lists past the fresh-serve floor and the
	// provider-failure fail-fast, so recovery is not blocked by the 30s backoff
	// a recent background failure just wrote. Three cases qualify:
	//   - a user relink (force_relink), unbound: the original outage recovery;
	//   - a deliberate candidate rotation (rotateCandidates): a replan
	//     rehydration or a decode-rejection rotation, which is server-initiated
	//     recovery rather than a background poll;
	//   - an explicit recovery resolve (bypassProviderFloor): the automatic
	//     alternate-version fallback walk.
	// Background liveness pollers and ordinary automatic resolves never set any
	// of these and stay on the floor. The resolve's own startup budget still
	// bounds every attempt.
	if (forceRelist && !options.sessionBound) || options.rotateCandidates || bypassProviderFloor {
		attemptCtx = virtuallibrary.WithProviderOutageRelist(attemptCtx)
	}
	attemptCtx = withVirtualCandidateRotationV3(attemptCtx, rotateCandidates)
	attemptCtx = withVirtualSessionBindingV3(attemptCtx, options.sessionBound)
	// An auto-picked quality profile that matches no candidate degrades to the
	// best-ranked candidate instead of hard-failing the start. Only a fresh,
	// non-explicit selection qualifies: a session binding must not swap the
	// release it is serving, and an explicit version pick keeps the refusal so
	// the client can offer the version list.
	attemptCtx = virtuallibrary.WithAutoProfileFallback(attemptCtx, !options.sessionBound && !options.explicitSelection)

	// fastPathHit records that resolveAndProbe returned the repeat-play fast
	// path so the candidate loop can return it immediately instead of treating
	// a deferred (ProbeSucceeded=false) pinned result as a failed candidate.
	fastPathHit := false
	// candidateProviderRelist carries the forced-relist intent for the candidate
	// being resolved. The provider is listed once per resolve, not once per
	// candidate: the first candidate pays the forced listing a recovery or
	// rotation asked for, and every later candidate resolves against that fresh
	// answer. Forcing a list per candidate multiplied one replan into one
	// multi-second provider round-trip per result (five sequential 2.6-4.8s
	// listings in production) while returning the same candidate set every time.
	candidateProviderRelist := providerRelist
	// incompleteMetadataFastPathSkip records that a stored-URL fast path was
	// refused because the row lacks planner-grade video evidence. The resolve
	// below can then merge the candidate's declared labels into
	// complete-looking evidence and take the skipProbe shortcut, publishing a
	// "verified" plan whose subtitle inventory is empty because no probe ever
	// enumerated it. Keep the probe admitted for this candidate instead.
	incompleteMetadataFastPathSkip := false

	resolveAndProbe := func(i int, cand VirtualPlaybackStream) (*resolvedVirtualPlaybackSource, error) {
		// requestedURI is the candidate this iteration asked the resolver for.
		// The detailed resolver may rewrite cand.URI to a substituted sibling
		// (a fresh-selection fall-through or a dedup keeper); keeping the
		// original lets every error name the candidate the attempt is actually
		// about instead of the substitute it happened to resolve to.
		requestedURI := cand.URI
		oid := cand.OwnerInstallationID
		if oid <= 0 {
			oid = file.VirtualOwnerInstallationID
		}
		// Repeat-play fast path. When the requested row already owns a pinned
		// or adopted provider-neutral candidate, complete probed evidence, and
		// a probe stamp, the start path does not need the provider URL: the
		// stream relay re-resolves at serve time and owns first-byte failover
		// (see stream.go and playback_transport.go), so a replay costs DB +
		// tokens only. Gated on the deferred start branch; the synchronous
		// replan/alternate/stale-fallback callers (deferProbe=false) still
		// resolve because they need probed track inventory to remap selections.
		//
		// The candidate must be the one the row actually points at. A
		// BestResultCache hit clears noResult for a neutral row and can re-rank a
		// different release to index 0; binding that release here would pin it
		// and copy the row's probed inventory onto a candidate that never
		// produced it.
		// The fast path skips the provider resolve, so it must not bypass the
		// caller's exclusion list or the known-bad stamp: with an exclusion
		// pending (decode rotation) or a failed row, fall through to the
		// resolve path, which honors both.
		// A collection-owned variant row may carry declared/complete-looking
		// evidence that was never produced by watching the live provider: its
		// release can vanish while the row keeps its stamp, and taking P0 would
		// bind the plan to the dead pin without ever re-listing. Only a row that
		// actually delivered bytes (or a non-collection row, whose evidence
		// comes from a real probe) may skip the provider round-trip. A never-
		// delivered collection row falls through to the resolve path, which
		// re-lists and can recover/rotate.
		collectionRowNeedsDelivery := file.ProbeSource == virtualCollectionProbeSource && file.LastDeliveredAt == nil
		p0Qualified := deferProbe && !forceRelist && !noResult &&
			len(excludedCandidateIDs) == 0 &&
			!collectionRowNeedsDelivery &&
			h.VirtualMediaDetailedResolver != nil &&
			((persistedResultURI && cand.URI == file.FilePath) || (pinnedURI != "" && cand.URI == pinnedURI)) &&
			file.ProbeUpdatedAt != nil &&
			completeVirtualVideoEvidenceV3(file) &&
			completeVirtualAudioEvidenceV3(file) &&
			completeVirtualContainerEvidenceV3(file)
		// Pre-validation gate. A row whose verdict damper is active, or whose
		// probe evidence has aged past the staleness bound, must not skip the
		// provider round-trip: it falls through to the normal resolve, which
		// re-lists, rematches by durable identity, adopts or rotates, and can
		// recover the row. This also covers an explicit retry (allowFailed) of a
		// failed row, which previously still took P0 and served the dead pin.
		// A fresh, non-failed row is untouched and keeps the fast path with no
		// extra provider call.
		p0Reason := p0PinSuspectReason(file, time.Now())
		p0Suspect := p0Qualified && p0Reason != ""
		if p0Suspect {
			logVirtualP0SuspectSkippedV3(attemptCtx, file, cand.URI, p0Reason)
		}
		if p0Qualified && p0Reason == "" {
			fastPathHit = true
			trace.fastPath = true
			transient := *file
			transient.FilePath = cand.URI
			transient.VirtualOwnerInstallationID = oid
			h.pinVirtualStickyAt(stickyKey, cand.URI, generation)
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
			return &resolvedVirtualPlaybackSource{
				URL: "", URI: cand.URI, OwnerID: oid, File: &transient,
				ProbeSucceeded: false, Provenance: ProbeProvenancePending,
				// Unranked: this path skipped the candidate list, so the
				// rank/count stay unknown rather than the zero value.
				CandidateRank: -1, CandidateCount: -1,
			}, nil
		}
		// Durable-resume fast path. The repeat-play gate above cannot apply
		// because the row's probe evidence is incomplete, but the row still
		// owns a concrete candidate with an unexpired persisted URL. After a
		// restart the in-memory pin and best-result cache are gone; the row is
		// the only surviving state. Binding this candidate now makes the
		// resumed session re-pin the same release the row names, and the serve
		// relay then uses the row's stored URL (the transport stored-URL
		// shortcut) instead of listing or resolving the provider. It never
		// substitutes: persistedResumeURI is the row's own candidate, so this
		// cannot change which candidate is served.
		//
		// It must not fire unless the row carries planner-grade video evidence:
		// a stored URL proves where the bytes live, not that the planner can
		// route them. Without the precondition a listing-written row with a URL
		// but empty tracks took this path, skipped resolve+probe, and terminalled
		// source_metadata_incomplete. The metadata gate forces the synchronous
		// resolve+probe at the bottom of this closure to fill the row in.
		durableResumeFastPath := deferProbe && !forceRelist && persistedResumeURI != "" && !noResult &&
			len(excludedCandidateIDs) == 0 && (allowFailed || !virtualCandidateVerdictActive(file.FailedAt, time.Now())) &&
			cand.URI == persistedResumeURI
		if durableResumeFastPath {
			if !virtualFastPathPlannerVideoCompleteV3(file) {
				logVirtualFastPathSkippedV3(attemptCtx, "durable_resume", file, cand.URI)
				incompleteMetadataFastPathSkip = true
			} else {
				fastPathHit = true
				trace.fastPath = true
				transient := *file
				transient.FilePath = cand.URI
				transient.VirtualOwnerInstallationID = oid
				h.pinVirtualStickyAt(stickyKey, cand.URI, generation)
				mergeVirtualCandidateTracks(&transient, cand)
				if !transient.HDR && cand.HDR != "" {
					transient.HDR = true
				}
				h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
				// The row has no probe stamp (it is exactly why this fast path
				// ran with incomplete evidence), so a provisional plan alone
				// would leave the inventory pending forever. Arm the
				// post-commit probe on the row's own stored URL: the client
				// still gets its URL immediately, and the worker resolves the
				// pending inventory to verified or failed with
				// inventory_updated. An already-stamped row skips this because
				// the P0 fast path above owns it.
				probeCand := cand
				if len(persistedResumeStored.RequestHeaders) > 0 {
					probeCand.RequestHeaders = cloneHeaderMap(persistedResumeStored.RequestHeaders)
				}
				deferred := h.virtualDeferredProbeForUnstampedRow(attemptCtx, stickyKey, file, persistedResumeStored.URL, probeCand, oid)
				// Pending is a promise that a worker will resolve the row, so
				// provenance follows the worker itself: only an admitted
				// deferred probe is pending. Every decline (the row already
				// carries a stamp, no prober is configured, the stored URL is
				// empty, or the probe-failure damper is armed) serves declared
				// metadata instead of a pending state nothing will resolve.
				// The damper shares its admission decision with the declared
				// fast path and runs the cache-only recovery; a fresh probe is
				// retried once the damper lapses.
				provenance := ProbeProvenanceDeclared
				if deferred != nil {
					provenance = ProbeProvenancePending
				}
				return &resolvedVirtualPlaybackSource{
					URL: "", URI: cand.URI, OwnerID: oid, File: &transient,
					ProbeSucceeded: false, Provenance: provenance,
					// Unranked: this path skipped the candidate list.
					CandidateRank: -1, CandidateCount: -1,
					DeferredProbe: deferred,
				}, nil
			}
		}
		// Optimistic start within the delivery grace. When the P0 gate above
		// cannot apply because the probe stamp is missing or the stored
		// evidence is incomplete, but the row already delivered bytes inside
		// the scanner's delivery grace, the candidate is still known-good.
		// Return the persisted URI now and revalidate in the background: the
		// serve relay re-resolves at serve time and owns first-byte failover
		// (see stream.go and playback_transport.go), while the background chain
		// resolves and probes so the next start takes the P0 fast path. Gated
		// on a real pinned/adopted candidate plus a configured resolver and
		// prober; no pin or no delivery grace keeps the synchronous resolve.
		//
		// Like durable resume, this must not fire without planner-grade video
		// evidence: the delivery grace proves the bytes flowed once, not that
		// the planner can route them now. And like the P0 gate above, it must
		// fire only for the exact candidate the row names: the grace proves a
		// sibling's bytes flowed, not this candidate's.
		optimisticFastPath := deferProbe && !forceRelist && !noResult &&
			len(excludedCandidateIDs) == 0 && (allowFailed || !virtualCandidateVerdictActive(file.FailedAt, time.Now())) &&
			(persistedResultURI || pinnedURI != "") &&
			cand.URI == fastPathEvidenceOwnerURI(file, persistedResultURI, pinnedURI) &&
			(h.VirtualMediaDetailedResolver != nil || h.VirtualPlaybackResolver != nil) &&
			(h.VirtualPlaybackSourceProber != nil || h.VirtualPlaybackSourceProberWithHeaders != nil) &&
			virtualDeliveredWithinGrace(file)
		if optimisticFastPath {
			if !virtualFastPathPlannerVideoCompleteV3(file) {
				logVirtualFastPathSkippedV3(attemptCtx, "delivery_grace", file, cand.URI)
				incompleteMetadataFastPathSkip = true
			} else {
				fastPathHit = true
				trace.fastPath = true
				transient := *file
				transient.FilePath = cand.URI
				transient.VirtualOwnerInstallationID = oid
				h.pinVirtualStickyAt(stickyKey, cand.URI, generation)
				mergeVirtualCandidateTracks(&transient, cand)
				if !transient.HDR && cand.HDR != "" {
					transient.HDR = true
				}
				h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
				h.revalidateVirtualCandidateBackground(r.Context(), stickyKey, file, cand, oid, userID, profileID, transient.ID)
				return &resolvedVirtualPlaybackSource{
					URL: "", URI: cand.URI, OwnerID: oid, File: &transient,
					ProbeSucceeded: false, Provenance: ProbeProvenancePending,
					// Unranked: this path skipped the candidate list.
					CandidateRank: -1, CandidateCount: -1,
				}, nil
			}
		}
		var streamURL string
		var resolveErr error
		// resolvedIdentity is the durable identity of the candidate the
		// resolver actually returned, populated below and carried on every
		// resolved source this iteration produces. It is what the probe
		// adoption write persists.
		var resolvedIdentity virtualProbeIdentity
		resolvedRematched := false
		trace.resolveRan = true
		resolveFetchBeforeDur, resolveFetchBeforeCalls := fetchSnapshot()
		resolveStart := time.Now()
		// probedCandidateID is the candidate this iteration asked the resolver
		// for. A fresh-selection fall-through can return a different one; when
		// it does, the probed candidate's declared metadata must not be served
		// under the resolved identity.
		probedCandidateID := virtualResultCandidateID(cand.URI)
		substituted := false
		// A persisted row the viewer is bound to (a session binding, or an
		// explicit version pick) is served from its own stored URL when that
		// URL is usable. Skipping the provider resolve is what lets an
		// explicitly selected candidate the provider stopped listing still
		// play; the probe below still runs, so an incomplete row cannot reach
		// the planner on declared-only stale metadata. A forced relink
		// deliberately bypasses this so the user gets a fresh URL.
		servedPersisted := !forceRelist && persistedResumeState == virtualStoredURLUsable &&
			persistedResumeStored.URL != "" && persistedResumeURI != "" &&
			cand.URI == persistedResumeURI && sameVirtualReleaseIdentity(file.FilePath, cand.URI) &&
			(options.sessionBound || explicitSelection)
		if servedPersisted {
			streamURL = persistedResumeStored.URL
			cand.RequestHeaders = cloneHeaderMap(persistedResumeStored.RequestHeaders)
			oid = effectiveVirtualOwner(persistedResumeStored.OwnerID, oid)
			selection := "explicit"
			if !explicitSelection {
				selection = "session_bound"
			}
			slog.InfoContext(attemptCtx, "virtual playback: serving a persisted candidate from its stored URL",
				"component", "api", "file_id", file.ID, "candidate_uri", cand.URI,
				"candidate_id", probedCandidateID, "window_state", "usable",
				"status", "selected_persisted", "selection", selection)
		} else if h.VirtualMediaDetailedResolver != nil {
			// Thread the row's durable identity and, inside the trust window,
			// the trusted flag so a delisted same-identity candidate is not
			// reported absent (which would let a pinned caller rotate to a
			// sibling under the viewer's selection). Only the row's own
			// release is annotated, and only for a pinned resolve (a session
			// binding or an explicit pick): an auto selection must not inherit
			// a trust that would suppress its fallback.
			candidateCtx := attemptCtx
			if sameVirtualReleaseIdentity(file.FilePath, cand.URI) && (options.sessionBound || explicitSelection) {
				candidateCtx = virtualResolveContextWithPersistedTrust(attemptCtx, file, time.Now(), h.virtualCandidateTrustWindow(), h.storedVirtualURLAllowInsecure(file, oid))
			}
			res, err := h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(
				candidateCtx, cand.URI, oid, userID, profileID, candidateProviderRelist, excludedCandidateIDs, preferredCandidateID,
			)
			if err == nil {
				streamURL = res.URL
				cand.RequestHeaders = cloneHeaderMap(res.RequestHeaders)
				resolvedIdentity = virtualProbeIdentity{
					VideoHash:   res.ProviderVideoHash,
					GUID:        res.ProviderGUID,
					ReleaseName: res.ProviderReleaseName,
					ReleaseSize: res.ProviderReleaseSize,
				}
				resolvedRematched = res.IdentityRematched
				resolvedID := res.CandidateID
				if resolvedID == "" && res.URI != "" {
					resolvedID = virtualResultCandidateID(res.URI)
				}
				substituted = resolvedID != "" && probedCandidateID != "" && resolvedID != probedCandidateID
				if res.URI != "" {
					cand.URI = res.URI
				}
				if res.CandidateID != "" {
					cand.ID = res.CandidateID
				}
				if substituted {
					// The resolver served a different release than the one
					// probed (a fresh-selection fall-through). Drop the probed
					// candidate's declared media identity so the transient and
					// the merge below cannot serve its resolution, codecs or
					// tracks under the resolved candidate; the resolved row or
					// the forced probe supplies the real metadata.
					resetSubstitutedCandidateMetadata(&cand)
				}
				// The provider that answered is the runtime owner for a
				// legacy file row whose stored owner is 0. Adopt it so the
				// transient file, the session, and every downstream recipe
				// carry the effective owner instead of 0.
				if res.OwnerID > 0 {
					oid = res.OwnerID
				}
			} else {
				resolveErr = err
			}
		} else if h.VirtualPlaybackResolver != nil {
			streamURL, resolveErr = h.VirtualPlaybackResolver.ResolveVirtualPlayback(
				attemptCtx, cand.URI, userID, profileID, oid,
			)
			cand.RequestHeaders = nil
		} else {
			resolveErr = errors.New("virtual playback resolver is not configured")
		}
		resolveDiscovery, resolveCalls := fetchSnapshot()
		resolveDiscovery -= resolveFetchBeforeDur
		resolveCalls -= resolveFetchBeforeCalls
		trace.resolve += virtualStageNetElapsed(resolveStart, resolveDiscovery)
		if resolveDiscovery > 0 {
			trace.fetch += resolveDiscovery
			trace.fetchRan = true
		}
		if resolveCalls > 0 {
			trace.fetchCalls += resolveCalls
		}
		if resolveErr != nil {
			// The trusted-absent sentinel means the provider re-list dropped a
			// candidate the viewer is still bound to. Recover the same release
			// from the row's own validated stored URL before giving up; when no
			// usable URL exists the resolve fails boundedly here, and the
			// candidate loop never substitutes a sibling.
			if errors.Is(resolveErr, virtuallibrary.ErrPersistedCandidateTrusted) {
				if recovered, ok := h.recoverTrustedStoredVirtualURL(attemptCtx, file, requestedURI, oid); ok {
					streamURL = recovered.URL
					cand.RequestHeaders = cloneHeaderMap(recovered.RequestHeaders)
					oid = effectiveVirtualOwner(recovered.OwnerID, oid)
					servedPersisted = true
					slog.InfoContext(attemptCtx, "virtual playback: recovered a trusted pinless candidate from its stored URL",
						"component", "api", "file_id", file.ID, "candidate_uri", requestedURI,
						"candidate_id", probedCandidateID, "status", "recovered_stored_url")
					resolveErr = nil
				}
			}
			if resolveErr != nil {
				return nil, resolveErr
			}
		}
		// URL syntax and SSRF validation happen in the provider service and
		// again when the relay opens the source. Do not perform a blocking body
		// fetch here; the provider may legitimately take time before its first
		// byte, and that would consume the entire startup budget.

		transient := *file
		transient.FilePath = cand.URI
		transient.VirtualOwnerInstallationID = oid
		dbFile, dbFound, dbLookupErr := h.lookupVirtualCandidateRowDetailed(attemptCtx, cand.URI, file.ContentID, file.EpisodeID, oid)
		if dbLookupErr != nil {
			// Fail closed: the lookup failure (or an incomplete row) leaves the
			// candidate's verdict unknown, so it must not be resolved or
			// adopted. This is the same policy the fallback's shared verdict
			// gate applies; the sentinel lets the caller's candidate loop stop
			// rather than reinterpret the unknown verdict as a dead candidate.
			return nil, fmt.Errorf("%w: candidate %s: %w", errVirtualCandidateVerdictUnknown, cand.URI, dbLookupErr)
		}
		// Enforce the supplied row's own failure stamp for its own release even
		// when no catalog row is found yet, mirroring the shared verdict gate.
		if !allowFailed && sameVirtualReleaseIdentity(file.FilePath, cand.URI) &&
			virtualCandidateVerdictActive(file.FailedAt, time.Now()) {
			return nil, fmt.Errorf("%w: candidate %s is marked failed", ErrVirtualCandidateMarkedFailed, cand.URI)
		}
		// A concrete ?result= candidate is served by the catalog row that owns
		// its exact URI, never by the provider-neutral sibling the neutral
		// fallback returns first (the lowest-id row sharing the neutral key).
		// An explicit pick of the same candidate serves that owner row
		// directly; an auto start must land on the same row, or the plan's
		// effective identity and evidence anchor would name the neutral
		// sibling while the transport streams the concrete candidate. The
		// owner lookup is the same exact-path resolution the deferred-probe
		// rotation uses, so the plan and the probe cannot disagree about the
		// row the bytes belong to. Only the seeded transient stays for a
		// genuinely neutral candidate (no ?result=), whose own row is the
		// requested one. The owner row then passes the same failed-verdict gate
		// below, so a concrete pick of an indicted row is still skipped.
		if virtualResultCandidateID(cand.URI) != "" &&
			(dbFile == nil || strings.TrimSpace(dbFile.FilePath) != strings.TrimSpace(cand.URI)) {
			if ownerRow, capable, ownerErr := h.virtualPathLookupRow(attemptCtx, cand.URI); capable && ownerErr == nil && ownerRow != nil {
				anchorIdentity, anchorHasIdentity := persistedVirtualIdentity(file)
				rowIdentity, rowHasIdentity := persistedVirtualIdentity(ownerRow)
				anchorHasURL := strings.TrimSpace(file.ResolvedURL) != ""
				admissible := true
				if anchorHasIdentity && rowHasIdentity && !persistedIdentityCompatible(anchorIdentity, rowIdentity) {
					admissible = false
				} else if anchorHasURL && !anchorHasIdentity && rowHasIdentity {
					admissible = false
				}
				if admissible {
					// Two rows of the same release share candidate URIs. The
					// exact-path owner is then a duplicate of the selected
					// version, not a different file: the requested row can
					// serve these same bytes and keeps its own inventory.
					// Rebinding the plan to the duplicate would rename the
					// version the viewer selected and replace its track menu
					// with the duplicate's possibly staler snapshot.
					if virtualSiblingOwnerSharesRelease(file, ownerRow) {
						slog.DebugContext(attemptCtx, "virtual playback: keeping the requested row for a same-release sibling owner",
							"component", "api", "requested_file_id", file.ID, "owner_file_id", ownerRow.ID,
							"candidate_uri", cand.URI, "reason", "same_release_owner")
					} else {
						dbFile = ownerRow
						dbFound = true
					}
				}
			}
		}
		if dbFound {
			// Auto-pick skips candidates whose catalog row is marked failed
			// (a transport produced no bytes, or the decoder rejected the
			// source, on a prior attempt). An explicit selection and a forced
			// relink allow a manual retry; a decode-driven rotation carries its
			// exclusion explicitly so it never depends on the async stamp.
			// The verdict binds only the candidate its own row names: a failed
			// sibling under the same neutral key must not veto this pick, and
			// the neutral fallback lookup returning the session's pinned row
			// must not re-indict a sibling with the pin's stamp. The candidate
			// is tried once and indicted under its own id if still dead.
			// Metadata adoption below is unaffected.
			if !allowFailed && virtualCandidateVerdictBindsRow(dbFile, cand.URI) &&
				virtualCandidateVerdictActive(dbFile.FailedAt, time.Now()) {
				// Name the candidate this attempt is about first. On a
				// substitution, cand.URI is the sibling the resolver selected,
				// so reporting only it made the log's candidate_uri and error
				// disagree about who failed; name both so an operator can
				// trust the attribution.
				if cand.URI != requestedURI {
					return nil, fmt.Errorf("%w: candidate %s resolved to %s, which is marked failed", ErrVirtualCandidateMarkedFailed, requestedURI, cand.URI)
				}
				return nil, fmt.Errorf("%w: candidate %s is marked failed", ErrVirtualCandidateMarkedFailed, cand.URI)
			}
			transient = *dbFile
			transient.FilePath = cand.URI
			transient.VirtualOwnerInstallationID = oid
		}
		// resolvedOwnsCandidate records whether the served transient is the
		// catalog row that owns the exact concrete candidate URI. The deferred
		// probe is bound to that row so its durable evidence write lands on the
		// same row the session will serve; a neutral candidate keeps the
		// requested row.
		resolvedOwnsCandidate := dbFound && dbFile != nil &&
			strings.TrimSpace(dbFile.FilePath) == strings.TrimSpace(cand.URI)
		// Carry the resolved candidate's durable identity on the served file
		// so the probe-evidence adoption persists the identity of the bytes
		// being adopted. Fill-empty-only: stored tiers are never overwritten.
		applyResolvedIdentity(&transient, resolvedIdentity)
		// Recovery coherence. A row the pre-validation gate sent down the
		// resolve path (active verdict or stale probe) that still resolves to
		// its own release is healthy again: clear the stale verdict through the
		// same fenced clear the versions check uses, so the next replay is
		// eligible for the P0 fast path instead of re-resolving on every start.
		// A substituted sibling carries its own verdict and is never cleared
		// here. A persisted row served straight from its stored URL never
		// contacted the provider, so it is not liveness evidence either. The
		// clear is fenced on the row's identity and observed verdict, so a
		// rotation or a newer failure survives it.
		if !servedPersisted && sameVirtualReleaseIdentity(file.FilePath, cand.URI) &&
			virtualCandidateVerdictActive(file.FailedAt, time.Now()) {
			h.clearVirtualCandidateVerdict(attemptCtx, file, cand.URI)
		}
		if substituted && (dbFile == nil || dbFile.ID <= 0) {
			// No catalog row for the resolved candidate: do not carry the
			// probed candidate's declared metadata onto it. The forced probe
			// below supplies the resolved bytes' metadata.
			clearVirtualCandidateDeclaredMetadata(&transient)
		}
		if transient.Duration <= 0 {
			if file.Duration > 0 {
				transient.Duration = file.Duration
			} else if file.EpisodeID != "" && h.EpisodeLookup != nil {
				if ep, err := h.EpisodeLookup.GetByID(attemptCtx, file.EpisodeID); err == nil && ep != nil && ep.Runtime > 0 {
					transient.Duration = ep.Runtime * 60
				}
			} else if file.ContentID != "" && h.ItemLookup != nil {
				if item, err := h.ItemLookup.GetByID(attemptCtx, file.ContentID); err == nil && item != nil && item.Runtime > 0 {
					transient.Duration = item.Runtime * 60
				}
			}
		}
		hasCompleteVideoEvidence := completeVirtualVideoEvidenceV3(&transient)
		hasCompleteAudioEvidence := completeVirtualAudioEvidenceV3(&transient)
		hasCompleteContainerEvidence := completeVirtualContainerEvidenceV3(&transient)
		skipProbe := hasCompleteVideoEvidence && hasCompleteAudioEvidence && hasCompleteContainerEvidence
		// A row that has never been probed carries NULL tracks and no probe
		// stamp. Candidate-declared metadata can synthesize complete-looking
		// evidence for the immediate plan, but it must not short-circuit the
		// real probe that stamps the row and persists its true track inventory.
		// Capture this before the candidate merge so a candidate-declared
		// inventory never counts as stored evidence.
		storedProbeMissing := transient.ProbeUpdatedAt == nil
		if substituted {
			// A substituted candidate has no trustworthy declared metadata left;
			// force the probe so the served file reflects the resolved bytes
			// rather than the probed candidate's resolution, codecs or tracks.
			skipProbe = false
			storedProbeMissing = true
		}
		if p0Suspect && (h.VirtualPlaybackSourceProber != nil || h.VirtualPlaybackSourceProberWithHeaders != nil) {
			// The P0 gate refused this row as suspect (active verdict or stale
			// probe). A URL re-resolve alone leaves probe_updated_at unchanged,
			// so without a fresh probe the row would be suspect on every later
			// replay and never return to the fast path. Force the real probe so
			// the persisted evidence and its stamp advance once validated.
			skipProbe = false
		}
		if !skipProbe && !incompleteMetadataFastPathSkip && cand.CodecVideo != "" && cand.Resolution != "" && cand.CodecAudio != "" && canSkipProbeForContainer(cand.Container) {
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			hasCompleteVideoEvidence = completeVirtualVideoEvidenceV3(&transient)
			hasCompleteAudioEvidence = completeVirtualAudioEvidenceV3(&transient)
			hasCompleteContainerEvidence = completeVirtualContainerEvidenceV3(&transient)
			skipProbe = hasCompleteVideoEvidence && hasCompleteAudioEvidence && hasCompleteContainerEvidence
		}
		if incompleteMetadataFastPathSkip {
			// The stored-URL fast path was refused for this row because its
			// planner video evidence is incomplete. The candidate's declared
			// labels can make the merged evidence look complete in the fields
			// the shortcut above checks, but a declaration never enumerated the
			// subtitle inventory: taking the shortcut would publish a
			// "verified" plan with an empty track menu and leave nothing to
			// repair it. Admit the probe instead. It either lands an inventory
			// now (bounded sync budget) or hands a deferred probe to the
			// committed session and keeps tracks_pending until it does.
			skipProbe = false
		}
		if skipProbe && !storedProbeMissing {
			h.pinVirtualStickyAt(stickyKey, cand.URI, generation)
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
			return withResolvedCandidate(&resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: true, Provenance: ProbeProvenanceVerified,
			}, resolvedIdentity, resolvedRematched), nil
		}
		allowDefer := allowDeferredProbe(deferProbe)
		if substituted {
			// The probed candidate's declared metadata is gone, so the deferred
			// declared-metadata path cannot be trusted; probe the resolved URL
			// synchronously and serve the bytes' real metadata.
			allowDefer = false
		}
		if p0Suspect && (h.VirtualPlaybackSourceProber != nil || h.VirtualPlaybackSourceProberWithHeaders != nil) {
			// Validate a suspect row synchronously so the refreshed probe stamp
			// is persisted before the response. A deferred probe would leave the
			// row suspect for the next start until the background write lands.
			allowDefer = false
		}
		if allowDefer {
			// Deferral guard: the deferred probe only runs after transport
			// commit, so merged metadata that cannot support a validated
			// route would terminal source_metadata_incomplete before the
			// probe ever runs (the bench Citizen Vigilante shape: stored URL,
			// empty tracks, fast ~50ms failure with no probe). Evaluate the
			// merged transient below; when it is route-plannable the
			// deferred path serves it immediately, otherwise fall through to
			// the bounded synchronous cache-first probe, which fills the row
			// in before the planner sees it. Damper admission is preserved:
			// a recent failure still takes the cache-only path.
			merged := cloneVirtualProbeTransient(transient)
			if merged.Resolution == "" {
				merged.Resolution = cand.Resolution
			}
			// No 1080p assumption here: the assumed baseline would make
			// the merge synthesize bitrate/codecs/tracks (all gated on
			// hasResolution) and render every row plannable-by-construction,
			// defeating the guard. Only stored + candidate evidence counts;
			// the deferred block below still applies the baseline for the
			// rows that pass.
			mergeVirtualCandidateTracks(&merged, cand)
			if !merged.HDR && cand.HDR != "" {
				merged.HDR = true
			}
			if !virtualMergedSupportsValidatedRoute(&merged) {
				allowDefer = false
			}
		}
		if allowDefer {
			h.pinVirtualStickyAt(stickyKey, cand.URI, generation)
			// Resolution precedence: stored evidence wins; otherwise adopt
			// the candidate's declared label; only when both are absent is
			// the 1080p baseline assumed. Only the last case marks
			// ResolutionAssumed, so a declared 2160p is never clobbered.
			if transient.Resolution == "" {
				transient.Resolution = cand.Resolution
			}
			resolutionAssumed := transient.Resolution == ""
			if resolutionAssumed {
				transient.Resolution = transcodeResolution1080p
			}
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
			// The ffprobe enumeration is deliberately not spawned inline on the
			// fresh-start path: the resolve hands the probe to the start path,
			// which spawns it only after the transport commit and the first-byte
			// URL. Session creation, recipe persistence, and the commit therefore
			// run without waiting on a multi-second remote ffprobe, and the
			// client gets its playable URL immediately. A caller that needs the
			// probed inventory on the response (a synchronous replan/rehydration)
			// does not opt in and keeps the inline probe below.
			var deferred *virtualDeferredProbeV3
			if h.VirtualPlaybackSourceProber != nil || h.VirtualPlaybackSourceProberWithHeaders != nil {
				probeKey := virtualProbeFailureKey(cand.URI, oid)
				probeTransient := cloneVirtualProbeTransient(transient)
				// Bind the post-commit probe to the row the plan actually
				// serves. When the resolved candidate's exact owner row is
				// served, its evidence write must stamp that row: the session
				// is bound to it (effective id and evidence anchor), so probing
				// the requested neutral sibling instead would stamp a row the
				// session never serves and the generation-fenced overlay
				// propagation would refuse, freezing the menus on the
				// provisional inventory for the whole session. A genuinely
				// neutral candidate keeps the requested row, which is its own.
				probeCatalogFile := file
				if resolvedOwnsCandidate {
					probeCatalogFile = &transient
				}
				// Zero the duration so the deferred probe measures the
				// empirical duration instead of inheriting the catalog value.
				probeTransient.Duration = 0
				if virtualProbeBackedOff(probeKey) {
					// A fresh failure already consumed the probe budget. The
					// inner probe may still have completed and landed in the
					// cache, so try a cache-only recovery before falling back
					// to declared metadata; otherwise the row stays unprobed
					// until the damper lapses.
					h.recoverVirtualProbeFromCache(r.Context(), probeCatalogFile, streamURL, probeTransient, cand, resolvedIdentity.tiers(), oid)
					return withResolvedCandidate(&resolvedVirtualPlaybackSource{
						URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: false, Provenance: ProbeProvenanceDeclared, ResolutionAssumed: resolutionAssumed,
					}, resolvedIdentity, resolvedRematched), nil
				}
				if options.deferProbePastCommit {
					deferred = &virtualDeferredProbeV3{
						stickyKey:              stickyKey,
						file:                   probeCatalogFile,
						streamURL:              streamURL,
						probeTransient:         &probeTransient,
						cand:                   cand,
						candidate:              resolvedIdentity,
						expectedRuntimeMinutes: h.virtualExpectedRuntimeMinutes(r.Context(), file),
						ownerID:                oid,
					}
				} else {
					probeCand := cand
					expectedRuntimeMinutes := h.virtualExpectedRuntimeMinutes(r.Context(), file)
					if gate := h.detachedGate(); gate.tryAcquire() {
						bgCtx, bgCancel := h.virtualDetachedContext(r.Context(), h.probeBackgroundBudget())
						go func() {
							defer gate.release()
							defer bgCancel()
							h.probeVirtualSourceAndPersist(bgCtx, stickyKey, probeCatalogFile, streamURL, probeTransient, probeCand, resolvedIdentity.tiers(), expectedRuntimeMinutes, oid)
						}()
					} else if !skipProbe {
						h.probeVirtualCandidateForegroundFallback(r.Context(), stickyKey, probeCatalogFile, streamURL, probeTransient, probeCand, resolvedIdentity.tiers(), expectedRuntimeMinutes, oid)
					}
				}
			}
			return withResolvedCandidate(&resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: false, Provenance: ProbeProvenancePending, ResolutionAssumed: resolutionAssumed,
				DeferredProbe: deferred,
			}, resolvedIdentity, resolvedRematched), nil
		}
		if h.VirtualPlaybackSourceProber == nil && h.VirtualPlaybackSourceProberWithHeaders == nil {
			// Resolution precedence: stored evidence wins; otherwise adopt
			// the candidate's declared label; only when both are absent is
			// the 1080p baseline assumed. Only the last case marks
			// ResolutionAssumed, so a declared 2160p is never clobbered.
			if transient.Resolution == "" {
				transient.Resolution = cand.Resolution
			}
			resolutionAssumed := transient.Resolution == ""
			if resolutionAssumed {
				transient.Resolution = transcodeResolution1080p
			}
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
			return withResolvedCandidate(&resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: false, Provenance: ProbeProvenanceDeclared, ResolutionAssumed: resolutionAssumed,
			}, resolvedIdentity, resolvedRematched), nil
		}
		probeKey := virtualProbeFailureKey(cand.URI, oid)
		// mergeDeclaredFallback backfills the candidate's declared metadata onto
		// the transient plan file and returns the resolved source under the given
		// provenance. Resolution precedence: stored evidence wins; otherwise
		// adopt the candidate's declared label; only when both are absent is the
		// 1080p baseline assumed. Only the last case marks ResolutionAssumed, so a
		// declared 2160p is never clobbered.
		//
		// A partial enumeration reuses it with pending provenance: the candidate's
		// bytes are not condemned, it keeps playing on its provisional plan, and no
		// probe evidence is published.
		mergeDeclaredFallback := func(provenance ProbeProvenance) (*resolvedVirtualPlaybackSource, error) {
			if transient.Resolution == "" {
				transient.Resolution = cand.Resolution
			}
			resolutionAssumed := transient.Resolution == ""
			if resolutionAssumed {
				transient.Resolution = transcodeResolution1080p
			}
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
			return withResolvedCandidate(&resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: false, Provenance: provenance, ResolutionAssumed: resolutionAssumed,
			}, resolvedIdentity, resolvedRematched), nil
		}
		declaredFallback := func() (*resolvedVirtualPlaybackSource, error) {
			return mergeDeclaredFallback(ProbeProvenanceFailed)
		}
		if skipProbe && !substituted {
			if transient.Resolution == "" {
				transient.Resolution = cand.Resolution
			}
			resolutionAssumed := transient.Resolution == ""
			if resolutionAssumed {
				transient.Resolution = transcodeResolution1080p
			}
			mergeVirtualCandidateTracks(&transient, cand)
			if !transient.HDR && cand.HDR != "" {
				transient.HDR = true
			}
			h.maybeTriggerSubtitleSearch(attemptCtx, &transient, cand)
			if virtualProbeBackedOff(probeKey) {
				// Damper armed: a recent failure already consumed the probe
				// budget. No new probe is admitted; try cache-only recovery
				// and serve declared metadata immediately.
				syncProbeFile := cloneVirtualProbeTransient(transient)
				syncProbeFile.Duration = 0
				h.recoverVirtualProbeFromCache(attemptCtx, file, streamURL, syncProbeFile, cand, resolvedIdentity.tiers(), oid)
			} else {
				probeBgFile := cloneVirtualProbeTransient(transient)
				probeBgFile.Duration = 0
				probeBgCand := cand
				if gate := h.detachedGate(); gate.tryAcquire() {
					bgCtx, bgCancel := h.virtualDetachedContext(r.Context(), h.probeBackgroundBudget())
					go func() {
						defer gate.release()
						defer bgCancel()
						h.probeVirtualSourceAndPersist(bgCtx, stickyKey, file, streamURL, probeBgFile, probeBgCand, resolvedIdentity.tiers(), h.virtualExpectedRuntimeMinutes(r.Context(), file), oid)
					}()
				}
			}
			return withResolvedCandidate(&resolvedVirtualPlaybackSource{
				URL: streamURL, URI: cand.URI, OwnerID: oid, File: &transient, ProbeSucceeded: false, Provenance: ProbeProvenanceDeclared, ResolutionAssumed: resolutionAssumed,
			}, resolvedIdentity, resolvedRematched), nil
		}
		if virtualProbeBackedOff(probeKey) {
			// A recent probe failure already consumed the probe budget. A
			// completed inner probe may still be cached; recover it before
			// settling for the candidate-declared metadata.
			syncProbeFile := cloneVirtualProbeTransient(transient)
			syncProbeFile.Duration = 0
			h.recoverVirtualProbeFromCache(attemptCtx, file, streamURL, syncProbeFile, cand, resolvedIdentity.tiers(), oid)
			return declaredFallback()
		}
		// Cache-first probe. The probe cache is keyed on the canonical candidate
		// URI plus a query-stripped provider URL, so an earlier resolution of
		// this exact candidate — the plan that is still playing — already holds
		// the inventory this synchronous probe would re-measure. On an
		// audio-switch or version-switch replan the source bytes are unchanged,
		// so paying the ffprobe again is pure latency; serve the cached evidence
		// and persist it with a fresh stamp. The cache never fabricates a
		// verdict: a miss falls through to the real probe below, and a
		// resolver-substituted sibling (a different release whose declared
		// metadata was already dropped) is never served from the requested
		// candidate's cache entry.
		syncProbeFile := cloneVirtualProbeTransient(transient)
		// Zero the duration so a real probe measures the empirical duration
		// instead of inheriting the catalog value.
		syncProbeFile.Duration = 0
		if !substituted {
			if cached, cachedObservation := h.virtualProbeFromCache(attemptCtx, file, streamURL, syncProbeFile, cand, resolvedIdentity.tiers(), oid); cached != nil {
				trace.probeRan = true
				if transient.ID > 0 {
					cached.ID = transient.ID
					cached.MediaFolderID = transient.MediaFolderID
				}
				if transient.Duration > 0 && cached.Duration <= 0 {
					cached.Duration = transient.Duration
				}
				applyResolvedIdentity(cached, resolvedIdentity)
				h.maybeTriggerSubtitleSearch(attemptCtx, cached, cand)
				// The hit is a completed probe, not necessarily the pin's
				// identity: the cached bytes can disagree with a served-size
				// observation recorded since they were measured, and
				// virtualProbeFromCache serves that case without writing. Carry
				// its verdict on the source so the finalization below can honor
				// it instead of classifying the same bytes a second time.
				return withResolvedCandidate(&resolvedVirtualPlaybackSource{
					URL: streamURL, URI: cand.URI, OwnerID: oid, File: cached, ProbeSucceeded: true, Provenance: ProbeProvenanceVerified,
					EvidenceObservation: cachedObservation,
				}, resolvedIdentity, resolvedRematched), nil
			}
		}
		probeCtx, probeCancel := context.WithTimeout(attemptCtx, virtualProbeBudget)
		trace.probeRan = true
		probeStart := time.Now()
		forwardVirtualCandidateDeclaredAudio(&syncProbeFile, cand)
		syncProbeFile.VirtualExpectedRuntimeMinutes = h.virtualExpectedRuntimeMinutes(attemptCtx, file)
		probeWait := startVirtualResolveWaitLog(probeCtx, "probe", "ffprobe", file.ContentID, file.ID, cand.URI,
			virtualProbeBudget, virtualStartProbeBudget)
		probed, probeErr := h.probeVirtualSource(probeCtx, streamURL, &syncProbeFile, cand.RequestHeaders)
		probeWait.finish(probeErr)
		trace.probe += time.Since(probeStart)
		probeCancel()
		if probeErr != nil || probed == nil {
			if errors.Is(probeErr, scanner.ErrVirtualProbePartial) {
				// The enumeration is incomplete, not the candidate invalid: the
				// candidate keeps its pin and the failure damper is not consumed,
				// and nothing is persisted or published as verified. This
				// synchronous path runs before the session exists, so it has no
				// deferred lifecycle for the convergence watchdog to close;
				// returning pending here would leave the start with no path to a
				// terminal state. Settle on the candidate's declared metadata
				// (non-verified) instead, and bound the retry with the same
				// incomplete-enumeration streak the non-negotiated background
				// path uses: once the streak repeats, the failure damper arms so
				// later starts stop re-probing and reuse the declared disposition
				// rather than holding a start pending for the session's life.
				if virtualProbeFailures.bumpPartial(probeKey) >= virtualProbePartialRepeatThreshold {
					virtualProbeFailures.mark(probeKey)
					slog.WarnContext(r.Context(), "virtual stream probe enumeration still incomplete; settling on declared metadata",
						"component", "api", "candidate_uri", cand.URI, "file_id", file.ID,
						"attempts", virtualProbePartialRepeatThreshold)
				}
				slog.DebugContext(r.Context(), "virtual stream probe enumeration incomplete; serving declared metadata",
					"component", "api", "candidate_uri", cand.URI, "error", probeErr)
				resolved, err := mergeDeclaredFallback(ProbeProvenanceDeclared)
				if resolved != nil {
					resolved.IncompleteEnumeration = true
				}
				return resolved, err
			}
			// A playback start that cancels this probe (the client went away,
			// the attempt was superseded, or the outer budget fired) leaves
			// the candidate's health unknown; the probe runs under its own
			// timeout and may still land in the cache. Marking the damper here
			// would poison the candidate for every later start and serve an
			// empty declared inventory that nothing repairs.
			if !virtualProbeVerdictUnknown(probeErr) {
				virtualProbeFailures.mark(probeKey)
			}
			slog.DebugContext(r.Context(), "virtual stream probe timed out or failed; using candidate metadata", "component", "api", "candidate_uri", cand.URI, "error", probeErr)
			return declaredFallback()
		}
		virtualProbeFailures.clear(probeKey)
		virtualProbeFailures.clearSizeMismatch(probeKey)
		if transient.ID > 0 {
			probed.ID = transient.ID
			probed.MediaFolderID = transient.MediaFolderID
		}
		if transient.Duration > 0 && probed.Duration <= 0 {
			probed.Duration = transient.Duration
		}
		mergeVirtualCandidateTracks(probed, cand)
		applyResolvedIdentity(probed, resolvedIdentity)
		h.maybeTriggerSubtitleSearch(probeCtx, probed, cand)
		return withResolvedCandidate(&resolvedVirtualPlaybackSource{
			URL: streamURL, URI: cand.URI, OwnerID: oid, File: probed, ProbeSucceeded: true, Provenance: ProbeProvenanceVerified,
		}, resolvedIdentity, resolvedRematched), nil
	}

	var firstResolved *resolvedVirtualPlaybackSource
	var attemptErr error
	for i, candidate := range candidates {
		candidateProviderRelist = providerRelist && i == 0
		result, err := resolveAndProbe(i, candidate)
		if fastPathHit {
			// The repeat-play fast path already committed the persisted
			// candidate; return it without re-ranking or unpinning the pin.
			// The rank/count describe the candidate list this start ranked,
			// which the fast path skipped: keep them unknown (-1) rather than
			// stamping a rank 0 into a list that was never measured.
			if result == nil {
				return resolvedVirtualPlaybackSource{}, errors.New("virtual playback fast path returned no source")
			}
			return *result, nil
		}
		if err != nil || result.Provenance == ProbeProvenanceFailed {
			if err != nil && errors.Is(err, errVirtualCandidateVerdictUnknown) {
				// The catalog could not answer for this candidate. The pin is
				// not known-bad and a sibling is not a valid substitute while
				// the verdict is unknowable, so stop instead of rotating.
				return resolvedVirtualPlaybackSource{}, err
			}
			if err != nil && errors.Is(err, virtuallibrary.ErrPersistedCandidateTrusted) {
				// Inside the trust window a delisted persisted candidate is
				// still the viewer's selection. Stop instead of substituting a
				// sibling the loop would otherwise try next.
				return resolvedVirtualPlaybackSource{}, err
			}
			if candidate.URI == pinnedURI && h != nil {
				// The pinned source stopped working; release it so the next
				// start re-ranks candidates instead of retrying a dead URI.
				h.unpinVirtualSticky(stickyKey, candidate.URI)
			}
			if firstResolved == nil && result != nil {
				firstResolved = result
			}
			if err != nil {
				slog.WarnContext(r.Context(), "virtual playback candidate failed",
					"component", "api", "candidate_uri", candidate.URI, "candidate_index", i,
					"file_id", file.ID, "content_id", file.ContentID, "error", err)
			}
			attemptErr = errors.Join(attemptErr, err)
			continue
		}
		// A deferred result already fired its background probe; return it
		// now. The original loop kept scanning trailing candidates for a
		// verified sibling, but every extra iteration pays a full provider
		// resolve RPC (a plugin round-trip, seconds each) whose work the
		// loop then throws away — the final return is this same first
		// usable source anyway. An incomplete-enumeration result settled on
		// declared metadata for the same reason and must not be substituted:
		// the candidate's bytes were never condemned, so a sibling is not a
		// valid replacement. Declared results (no prober) fall through to the
		// finalization block below so they still get the runtime check,
		// evidence persist, and sticky pin.
		if result.Provenance == ProbeProvenancePending || result.IncompleteEnumeration {
			result.CandidateRank = i
			result.CandidateCount = len(candidates)
			return *result, nil
		}
		if result.Provenance == ProbeProvenanceVerified || (!result.ResolutionAssumed && h.VirtualPlaybackSourceProber == nil && h.VirtualPlaybackSourceProberWithHeaders == nil) {
			// Content ground truth: a probed duration wildly different from the
			// catalog runtime means the provider handed us mislabeled content.
			// Skip persisting its metadata onto this content's rows and rotate.
			expectedRuntimeMinutes := 0
			if file.EpisodeID != "" && h.EpisodeLookup != nil {
				if ep, epErr := h.EpisodeLookup.GetByID(r.Context(), file.EpisodeID); epErr == nil && ep != nil {
					expectedRuntimeMinutes = ep.Runtime
				}
			}
			if expectedRuntimeMinutes == 0 && h.ItemLookup != nil {
				if item, itemErr := h.ItemLookup.GetByID(r.Context(), file.ContentID); itemErr == nil && item != nil {
					expectedRuntimeMinutes = item.Runtime
				}
			}
			ownerID := candidate.OwnerInstallationID
			if ownerID <= 0 {
				ownerID = file.VirtualOwnerInstallationID
			}
			// Identity is decided by the measured evidence, not the declared
			// size: a size gap stays usable but unverified, while a duration or
			// track-layout contradiction identifies a different file.
			evidencePin := scanner.VirtualServedSizePin(candidate.URI, ownerID)
			// A cache hit arrives with the verdict its own pass produced (see
			// virtualProbeFromCache). Classifying is not a read-only check: it
			// records the pin's first served-size observation, so a second pass
			// over the same bytes reads back the snapshot this request just
			// wrote, takes its own recording for the confirming serve, and hands
			// an unconfirmed serve to the persist and pin below. Honor the
			// pre-set observation instead. A mismatch never reaches this branch:
			// the cache seam discards it, so the observation is the only verdict
			// a hit can carry that the gate must not recompute.
			evidenceVerdict := scanner.VirtualServedEvidenceUnverified
			if !result.EvidenceObservation {
				evidenceVerdict = classifyVirtualServedEvidenceV3(
					evidencePin, result.File.FileSize, virtualDeclaredProbeFile(file, candidate), result.File, expectedRuntimeMinutes,
				)
			}
			sizeGap := scanner.VirtualServedSizeMismatch(result.File.FileSize, result.File.ProbedFormatSize)
			if !virtualRuntimePlausible(result.File.Duration, expectedRuntimeMinutes) && !sizeGap {
				// A matching declared size with an impossible runtime is a
				// different file: it keeps the historical duration rejection and
				// its dead-candidate damper.
				slog.WarnContext(r.Context(), "virtual candidate rejected: probed duration implausible",
					"component", "api", "candidate_uri", candidate.URI,
					"file_id", file.ID, "content_id", file.ContentID,
					"probed_duration_seconds", result.File.Duration,
					"expected_runtime_minutes", expectedRuntimeMinutes)
				h.unpinVirtualSticky(stickyKey, candidate.URI)
				attemptErr = errors.Join(attemptErr, fmt.Errorf("candidate %s probed duration %ds implausible for %dm runtime",
					candidate.URI, result.File.Duration, expectedRuntimeMinutes))
				continue
			}
			if evidenceVerdict == scanner.VirtualServedEvidenceMismatch {
				// The probe found positive evidence this is not the release
				// the row declares (a duration contradiction with a size gap, or
				// a track-layout contradiction). Rotate to a sibling instead of
				// persisting a different release's inventory, but keep the
				// wrong-source damper short so this row is retried promptly once
				// the provider serves it again. The row is never stamped failed
				// here: the release is not dead.
				virtualProbeFailures.markSizeMismatch(virtualProbeFailureKey(candidate.URI, ownerID), result.File.ProbedFormatSize, result.File.FileSize)
				logVirtualProbeSizeMismatch(r.Context(), file.ID, candidate.URI, result.File.FileSize, result.File.ProbedFormatSize)
				h.unpinVirtualSticky(stickyKey, candidate.URI)
				attemptErr = errors.Join(attemptErr, fmt.Errorf("candidate %s is not the declared release (probed %d bytes, declared %d)",
					candidate.URI, result.File.ProbedFormatSize, result.File.FileSize))
				// The same release is usually offered by several indexers under
				// separate candidate ids. Each is an independent dice roll at
				// the provider's cache, so probe the release's siblings before
				// this start gives up; a sibling's verified inventory is
				// persisted onto the sibling row, never this one.
				if sibling := h.fallbackSameReleaseAfterServedSizeMismatch(
					attemptCtx, file, candidate, candidates, stickyKey, generation,
				); sibling != nil {
					return *sibling, nil
				}
				continue
			}
			if sizeGap {
				logVirtualProbeSizeGapV3(r.Context(), file.ID, candidate.URI, result.File.FileSize, result.File.ProbedFormatSize, result.File.Duration, expectedRuntimeMinutes,
					virtualProbeEvidenceWithholdsIdentity(evidenceVerdict))
				virtualProbeFailures.clearSizeMismatch(virtualProbeFailureKey(candidate.URI, ownerID))
			}
			// A rotation adopts the identity of the row that owns the served
			// bytes, but it must not cost the viewer the file they selected.
			// Reject a candidate whose effective row contradicts the item's
			// runtime or would offer fewer audio or subtitle tracks than the
			// selected row. The next candidate may be the selected release
			// itself, and a wrong or track-poor substitute is worse than an
			// honest failure. The duration clause keeps the rule intact on a
			// surface that excuses a size-gapped probe from its own duration
			// guard: tolerance for a posted-short release is not a license to
			// serve a different one. This also keeps a size-gapped serve of
			// the requested row's own bytes on the requested row (its
			// effective id equals the requested id, so the guard does not
			// fire).
			if result.File.ID > 0 && result.File.ID != file.ID {
				rotationReason := ""
				switch {
				case !virtualRuntimePlausible(result.File.Duration, expectedRuntimeMinutes):
					rotationReason = "duration_contradiction"
				case virtualRotationWouldDropTracks(file, result.File, result.File):
					rotationReason = "track_loss"
				}
				if rotationReason != "" {
					slog.WarnContext(r.Context(), "virtual candidate rotation refused: effective row contradicts the selected release",
						"component", "api", "requested_file_id", file.ID, "effective_file_id", result.File.ID,
						"candidate_uri", candidate.URI, "reason", rotationReason,
						"probed_duration_seconds", result.File.Duration, "expected_runtime_minutes", expectedRuntimeMinutes,
						"selected_audio_tracks", len(file.AudioTracks), "selected_subtitle_tracks", len(file.SubtitleTracks),
						"effective_audio_tracks", len(result.File.AudioTracks), "effective_subtitle_tracks", len(result.File.SubtitleTracks))
					h.unpinVirtualSticky(stickyKey, candidate.URI)
					attemptErr = errors.Join(attemptErr, fmt.Errorf("candidate %s rotates to row %d (%s)",
						candidate.URI, result.File.ID, rotationReason))
					continue
				}
			}
			// A genuine rotation (the selected row cannot serve) stays a
			// substitution and says so, so the client can report which
			// version it got instead of silently replacing the file the
			// viewer picked.
			if result.File.ID > 0 && result.File.ID != file.ID && virtualRequestedRowIsSelection(file) {
				result.SubstitutedFromFileID = file.ID
				result.SubstitutionReason = substitutionReasonVersionNotPlayableV3
			}
			result.CandidateRank = i
			result.CandidateCount = len(candidates)
			withholdsIdentity := result.EvidenceObservation || virtualProbeEvidenceWithholdsIdentity(evidenceVerdict)
			if withholdsIdentity {
				// The bytes play for this request and their measured inventory
				// belongs on the row: write it metadata-only so the versions
				// list and action bar can report the real tracks. Do not stamp
				// the probe or pin the candidate — that would make one
				// unconfirmed serve the pin's identity. A cached hit carries the
				// same disposition from its own pass.
				h.persistVirtualProbeEvidence(r.Context(), file, result.File.FilePath, result.File, resolvedSourceIdentity(result).tiers(), false, true)
				return *result, nil
			}
			h.persistVirtualProbeEvidence(r.Context(), file, result.File.FilePath, result.File, resolvedSourceIdentity(result).tiers(), result.Provenance == ProbeProvenanceVerified, true)
			// The filtered candidate list is already cached device-neutrally
			// above (and ranked for this device), so replays skip the provider
			// round-trip and re-rank for the requesting device. Pin this URI
			// as sticky so rotation cannot churn future sessions.
			h.pinVirtualStickyAt(stickyKey, candidate.URI, generation)
			return *result, nil
		}
		if firstResolved == nil {
			copy := *result
			copy.CandidateRank = i
			copy.CandidateCount = len(candidates)
			firstResolved = &copy
		}
	}
	if firstResolved != nil {
		// Re-merge against the candidate that actually produced this result,
		// not candidates[0]: after failover the usable source may come from
		// a later candidate, and merging the wrong candidate contaminates
		// tracks with another release's metadata.
		for _, candidate := range candidates {
			if firstResolved.URI != "" && candidate.URI == firstResolved.URI {
				mergeVirtualCandidateTracks(firstResolved.File, candidate)
				break
			}
		}
		if firstResolved.Provenance == ProbeProvenanceVerified {
			// An observation already carries a completed probe, so its inventory
			// is written, but it must not be stamped as the row's identity.
			h.persistVirtualProbeEvidence(r.Context(), file, firstResolved.File.FilePath, firstResolved.File, resolvedSourceIdentity(firstResolved).tiers(), !firstResolved.EvidenceObservation, true)
		}
		// The rank/count were set where this result was produced; a
		// fast-path result that skipped ranking keeps its unknown (-1).
		return *firstResolved, nil
	}
	if attemptErr == nil {
		attemptErr = errors.New("virtual playback provider returned no usable stream")
	}
	// When the primary resolution fails — commonly because a previously
	// persisted "result=" candidate has rotated or expired at the provider —
	// re-rank the current provider candidates provider-neutrally and retry
	// before failing. This keeps one stale indexer/debrid result from turning
	// a still-streamable item into a hard playback failure, without crossing
	// the user's selected quality when a same-profile candidate exists.
	// The stale-source fallback does its own provider re-list internally; time
	// it as one stage so a fallback-driven resolve is attributable rather than
	// silently folded into the loop's resolve time.
	trace.fallbackRan = true
	fallbackStart := time.Now()
	// Carry the resolve intent explicitly into fallback eligibility rather
	// than letting the fallback read defaulted context values. A session-bound
	// start may refresh its own release but may not substitute a sibling
	// unless this request declared rotation; an unbound (fresh) start may
	// substitute normally. allowFailed is the explicit-retry policy for a
	// failed_at verdict and never authorizes a sibling by itself.
	//
	// A session-bound request anchors on the immutable session source URI, not
	// the persisted row's file_path: the row may have been rewritten by an
	// earlier adoption, and the fallback must validate the release against what
	// the session is actually serving. Only when the caller supplies no anchor
	// (a fresh/unbound resolve, or a legacy caller) does the release fall back
	// to the row-derived identity.
	anchoredReleaseID := virtualResultCandidateID(file.FilePath)
	if options.sessionBound && strings.TrimSpace(options.sessionAnchorURI) != "" {
		anchoredReleaseID = virtualResultCandidateID(options.sessionAnchorURI)
	}
	var fallbackAnchorErr error
	fallbackEligibility := virtualFallbackEligibility{
		sessionBound:         options.sessionBound,
		rotationAllowed:      rotateCandidates,
		allowFailed:          allowFailed,
		releaseID:            anchoredReleaseID,
		excludedCandidateIDs: append([]string(nil), excludedCandidateIDs...),
		anchorErr:            &fallbackAnchorErr,
	}
	fb := h.fallbackResolveStaleVirtualSource(attemptCtx, file, userID, profileID, fallbackEligibility)
	trace.fallback = time.Since(fallbackStart)
	if fb != nil {
		return *fb, nil
	}
	if fallbackAnchorErr != nil {
		attemptErr = errors.Join(attemptErr, fallbackAnchorErr)
	}
	return resolvedVirtualPlaybackSource{}, attemptErr
}

// virtualServedSizeMismatchFallbackLimit bounds how many same-release sibling
// candidates one served-size-mismatch fallback may probe. A release can be
// offered by many indexers, each row an independent roll of the provider's
// cache dice, but every attempt costs a provider resolve plus ffprobe, so the
// fallback stops here rather than multiplying probe work with the indexer
// count.
const virtualServedSizeMismatchFallbackLimit = 3

// fallbackSameReleaseAfterServedSizeMismatch adapts the shared sibling probe to
// the synchronous start path. matchSameReleaseSiblingProbe does the bounded
// same-release probing and identity acceptance; this adopts the sibling's bytes
// by persisting its inventory on the sibling's own row and returning it as the
// effective source. An unverified sibling (a served-size gap) still serves and
// writes its inventory, but is not pinned: its bytes are usable, not identity.
// The candidate loop's outer behavior is unchanged: this runs only on a
// mismatch and returns nil when no sibling is accepted so the caller continues
// exactly as before.
//
// The selected row keeps the short wrong-source damper armed by its caller;
// each rejected sibling arms the same short damper, and none is stamped
// failed_at — the release is not dead, the provider's cache served the wrong
// file.
func (h *PlaybackHandler) fallbackSameReleaseAfterServedSizeMismatch(
	ctx context.Context,
	file *models.MediaFile,
	anchor VirtualPlaybackStream,
	candidates []VirtualPlaybackStream,
	stickyKey string,
	generation uint64,
) *resolvedVirtualPlaybackSource {
	if h == nil || file == nil {
		return nil
	}
	ownerID := effectiveVirtualOwner(anchor.OwnerInstallationID, file.VirtualOwnerInstallationID)
	sibling := h.matchSameReleaseSiblingProbe(ctx, file, anchor, ownerID, stickyKey)
	if sibling == nil {
		return nil
	}
	if virtualRotationWouldDropTracks(file, sibling.row, sibling.probed) {
		slog.WarnContext(ctx, "virtual probe served size mismatch: refusing a same-release sibling that would lose audio or subtitle tracks",
			"component", "api", "file_id", file.ID, "sibling_file_id", sibling.row.ID,
			"candidate_uri", anchor.URI, "sibling_uri", sibling.cand.URI,
			"selected_audio_tracks", len(file.AudioTracks), "selected_subtitle_tracks", len(file.SubtitleTracks),
			"substitute_audio_tracks", len(sibling.probed.AudioTracks), "substitute_subtitle_tracks", len(sibling.probed.SubtitleTracks))
		return nil
	}
	h.persistVirtualProbeEvidence(ctx, sibling.row, sibling.probed.FilePath, sibling.probed,
		sibling.identity, !sibling.observation, true)
	if !sibling.observation {
		h.pinVirtualStickyAt(stickyKey, sibling.cand.URI, generation)
	}
	result := &resolvedVirtualPlaybackSource{
		URL:                 sibling.url,
		URI:                 sibling.cand.URI,
		OwnerID:             sibling.cand.OwnerInstallationID,
		File:                sibling.probed,
		ProbeSucceeded:      true,
		Provenance:          ProbeProvenanceVerified,
		EvidenceObservation: sibling.observation,
		ResolvedURL:         sibling.url,
		RequestHeaders:      sibling.headers,
		ProviderVideoHash:   sibling.identity.VideoHash,
		ProviderGUID:        sibling.identity.GUID,
		ProviderReleaseName: sibling.identity.ReleaseName,
		ProviderReleaseSize: sibling.identity.ReleaseSize,
		CandidateRank:       -1,
		CandidateCount:      len(candidates),
	}
	for i := range candidates {
		if sameVirtualCandidate(candidates[i].URI, sibling.cand.URI) {
			result.CandidateRank = i
			break
		}
	}
	slog.WarnContext(ctx, "virtual probe served size mismatch: adopted same-release sibling",
		"component", "api", "file_id", file.ID, "sibling_file_id", sibling.row.ID,
		"indexer", virtualCandidateIndexerLabel(sibling.cand), "candidate_uri", anchor.URI,
		"sibling_uri", sibling.cand.URI)
	return result
}

// virtualCandidateIndexerLabel names the indexer/source behind a picker
// candidate for fallback logging. The provider-neutral candidate shape carries
// no dedicated indexer field, so the concrete candidate id (which each indexer
// offer mints) and the URI's result id are preferred, then the release name.
func virtualCandidateIndexerLabel(candidate VirtualPlaybackStream) string {
	for _, value := range []string{candidate.ID, virtualResultCandidateID(candidate.URI), candidate.ProviderReleaseName, candidate.Label} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// virtualDeliveredWithinGrace reports whether the row's last transport
// delivery is inside the scanner's delivery grace. A nil stamp means the row
// never delivered, so the start path must resolve synchronously.
func virtualDeliveredWithinGrace(file *models.MediaFile) bool {
	if file == nil || file.LastDeliveredAt == nil {
		return false
	}
	return time.Since(*file.LastDeliveredAt) < scanner.VirtualCandidateDeliveryGrace
}

// fastPathEvidenceOwnerURI names the one candidate the row's persisted evidence
// belongs to: the row's own ?result= pick when it names a concrete candidate,
// else the in-memory sticky pin. Every fast path that copies the row's probed
// inventory onto a candidate must require cand.URI to equal this owner — the
// delivery grace proves the owner's bytes flowed, never a freshly ranked
// sibling's. Empty means no owner (neutral row, no pin): no fast path may fire.
func fastPathEvidenceOwnerURI(file *models.MediaFile, persistedResultURI bool, pinnedURI string) string {
	if file != nil && persistedResultURI && strings.TrimSpace(file.FilePath) != "" {
		return file.FilePath
	}
	return strings.TrimSpace(pinnedURI)
}

// cloneVirtualProbeTransient copies a MediaFile with its track slices
// deep-copied. The synchronous caller keeps the original while the background
// probe mutates its own copy, so the two must not share backing arrays.
func cloneVirtualProbeTransient(base models.MediaFile) models.MediaFile {
	clone := base
	if len(base.VideoTracks) > 0 {
		clone.VideoTracks = append([]models.VideoTrack(nil), base.VideoTracks...)
	}
	if len(base.AudioTracks) > 0 {
		clone.AudioTracks = append([]models.AudioTrack(nil), base.AudioTracks...)
	}
	if len(base.SubtitleTracks) > 0 {
		clone.SubtitleTracks = append([]models.SubtitleTrack(nil), base.SubtitleTracks...)
	}
	return clone
}

// virtualExpectedRuntimeMinutes resolves the catalog runtime used to sanity
// check a probed duration. It prefers the episode runtime and falls back to the
// content item runtime. Zero means no expectation is available.
func (h *PlaybackHandler) virtualExpectedRuntimeMinutes(ctx context.Context, file *models.MediaFile) int {
	if file == nil {
		return 0
	}
	expected := 0
	if file.EpisodeID != "" && h.EpisodeLookup != nil {
		if ep, err := h.EpisodeLookup.GetByID(ctx, file.EpisodeID); err == nil && ep != nil {
			expected = ep.Runtime
		}
	}
	if expected == 0 && file.ContentID != "" && h.ItemLookup != nil {
		if item, err := h.ItemLookup.GetByID(ctx, file.ContentID); err == nil && item != nil {
			expected = item.Runtime
		}
	}
	return expected
}

// virtualSiblingProbe is one same-release sibling row whose served bytes are
// accepted for that row: either matching its own declared size or short of it
// with a duration that proves identity. It is the probe chain's new target: the
// row, its candidate, the probe input, and the enumerated bytes all belong to
// the sibling, never the row the probe was originally asked about.
type virtualSiblingProbe struct {
	row       *models.MediaFile
	cand      VirtualPlaybackStream
	transient models.MediaFile
	probed    *models.MediaFile
	identity  resolver.PersistedIdentityTiers
	// observation marks bytes that are usable but do not yet establish the
	// sibling pin's release identity (a served-size gap). The inventory is
	// written metadata-only and the pin is not made sticky until a later serve
	// confirms it.
	observation bool
	// url and headers are the transport this probe opened. The start-path
	// fallback returns them on the adopted source; the background chain only
	// needs the probed inventory and ignores them.
	url     string
	headers map[string]string
}

// matchSameReleaseSiblingProbe probes the selected row's same-release sibling
// rows in picker order, stopping at the first whose served bytes are accepted
// for that row (size match, or a short posting whose duration proves identity).
// The version list supplies both the order and the release grouping, so this
// does not invent a second identity rule. It returns nil when no sibling is
// accepted, leaving the caller's original failure path untouched.
//
// Every attempt is bounded by the caller's existing probe context: no new
// timeout is introduced. A sibling whose bytes are rejected arms the same short
// wrong-source damper; no sibling is ever stamped failed_at.
func (h *PlaybackHandler) matchSameReleaseSiblingProbe(ctx context.Context, anchor *models.MediaFile, anchorCand VirtualPlaybackStream, ownerInstallationID int, rejectStickyKey ...string) *virtualSiblingProbe {
	rows := h.sameReleaseSiblingRows(ctx, anchor, anchorCand)
	for i, row := range rows {
		if i >= virtualServedSizeMismatchFallbackLimit {
			break
		}
		cand := virtualPlaybackStreamFromFile(row)
		if cand == nil || virtualResultCandidateID(strings.TrimSpace(row.FilePath)) == "" {
			continue
		}
		cand.OwnerInstallationID = effectiveVirtualOwner(cand.OwnerInstallationID, ownerInstallationID)
		// The stream builder copies the transport-facing fields; the durable
		// provider identity the evidence write and the same-release grouping
		// need stays on the row.
		cand.ProviderVideoHash = row.ProviderVideoHash
		cand.ProviderGUID = row.ProviderGUID
		cand.ProviderReleaseName = row.ProviderReleaseName
		if cand.FileSize <= 0 {
			cand.FileSize = row.ProviderReleaseSize
		}
		probeURL, probeHeaders, ok := h.siblingProbeURL(ctx, row)
		if !ok {
			continue
		}
		cand.RequestHeaders = probeHeaders
		slog.WarnContext(ctx, "virtual probe served size mismatch: trying same-release sibling",
			"component", "api", "file_id", catalogFileIDOrZero(anchor), "sibling_file_id", row.ID,
			"indexer", virtualCandidateIndexerLabel(*cand), "candidate_uri", anchorCand.URI,
			"sibling_uri", cand.URI)
		transient := cloneVirtualProbeTransient(*row)
		// Zero the duration so the sibling's probe measures the empirical
		// duration instead of inheriting the catalog value.
		transient.Duration = 0
		transient.VirtualExpectedRuntimeMinutes = h.virtualExpectedRuntimeMinutes(ctx, row)
		forwardVirtualCandidateDeclaredAudio(&transient, *cand)
		probed, err := h.probeVirtualSource(ctx, probeURL, &transient, probeHeaders)
		if err != nil || probed == nil {
			continue
		}
		siblingPin := scanner.VirtualServedSizePin(cand.URI, cand.OwnerInstallationID)
		verdict := classifyVirtualServedEvidenceV3(siblingPin, transient.FileSize, &transient, probed, transient.VirtualExpectedRuntimeMinutes)
		if !verdict.Usable() {
			// A positive identity contradiction (duration or track layout), not
			// a size gap: this sibling is a different file.
			virtualProbeFailures.markSizeMismatch(virtualProbeFailureKey(cand.URI, cand.OwnerInstallationID), probed.ProbedFormatSize, transient.FileSize)
			logVirtualProbeSizeMismatch(ctx, row.ID, cand.URI, transient.FileSize, probed.ProbedFormatSize)
			// The start path unpins a rejected sibling so a later start does not
			// retry the same wrong-byte row; the background chain passes no key
			// and keeps its existing behavior.
			if len(rejectStickyKey) > 0 {
				h.unpinVirtualSticky(rejectStickyKey[0], cand.URI)
			}
			continue
		}
		observation := !verdict.EstablishesIdentity()
		if scanner.VirtualServedSizeMismatch(transient.FileSize, probed.ProbedFormatSize) {
			if observation {
				logVirtualProbeSiblingObservationPending(ctx, row.ID, cand.URI, transient.FileSize, probed.ProbedFormatSize)
			} else {
				logVirtualProbeSizeMismatchAccepted(ctx, row.ID, cand.URI, transient.FileSize, probed.ProbedFormatSize, probed.Duration, transient.VirtualExpectedRuntimeMinutes)
			}
			virtualProbeFailures.clearSizeMismatch(virtualProbeFailureKey(cand.URI, cand.OwnerInstallationID))
		}
		return &virtualSiblingProbe{
			row:         row,
			cand:        *cand,
			transient:   transient,
			probed:      probed,
			identity:    resolver.NewPersistedIdentityTiers(cand.ProviderVideoHash, cand.ProviderGUID, cand.ProviderReleaseName, cand.FileSize),
			observation: observation,
			url:         probeURL,
			headers:     probeHeaders,
		}
	}
	return nil
}

// sameReleaseSiblingRows returns the selected row's same-release sibling rows in
// catalog order, capped at the fallback probe bound. The rows are the same
// pre-collapse set the version list builds from, and catalog.VirtualRowsShareRelease
// is the rule it uses to group them into one release, so "sibling" means exactly
// the rows the picker groups under this release. The selected row itself and the
// provider-neutral group row are never returned as concrete probe targets.
func (h *PlaybackHandler) sameReleaseSiblingRows(ctx context.Context, anchor *models.MediaFile, anchorCand VirtualPlaybackStream) []*models.MediaFile {
	if h == nil || anchor == nil || h.FileVersionFetcher == nil {
		return nil
	}
	var rows []*models.MediaFile
	var err error
	switch {
	case strings.TrimSpace(anchor.EpisodeID) != "":
		rows, err = h.FileVersionFetcher.GetByEpisodeID(ctx, anchor.EpisodeID)
	case strings.TrimSpace(anchor.ContentID) != "":
		rows, err = h.FileVersionFetcher.GetByContentID(ctx, anchor.ContentID)
	default:
		return nil
	}
	if err != nil || len(rows) == 0 {
		return nil
	}
	anchorRow := *anchor
	if anchorRow.FileSize <= 0 && anchorCand.FileSize > 0 {
		anchorRow.FileSize = anchorCand.FileSize
	}
	if anchorRow.ProviderReleaseSize <= 0 && anchorCand.FileSize > 0 {
		anchorRow.ProviderReleaseSize = anchorCand.FileSize
	}
	if strings.TrimSpace(anchorRow.ProviderVideoHash) == "" {
		anchorRow.ProviderVideoHash = anchorCand.ProviderVideoHash
	}
	if strings.TrimSpace(anchorRow.ProviderGUID) == "" {
		anchorRow.ProviderGUID = anchorCand.ProviderGUID
	}
	if strings.TrimSpace(anchorRow.ProviderReleaseName) == "" {
		anchorRow.ProviderReleaseName = anchorCand.ProviderReleaseName
	}
	siblings := make([]*models.MediaFile, 0, virtualServedSizeMismatchFallbackLimit)
	for _, row := range rows {
		if len(siblings) >= virtualServedSizeMismatchFallbackLimit {
			break
		}
		if row == nil || row.ID <= 0 || row.ID == anchor.ID || row.FilePath == anchor.FilePath ||
			strings.TrimSpace(row.FilePath) == strings.TrimSpace(anchorCand.URI) {
			continue
		}
		if !catalog.VirtualRowsShareRelease(&anchorRow, row) {
			continue
		}
		siblings = append(siblings, row)
	}
	return siblings
}

// siblingProbeURL resolves the transport a same-release sibling's probe must
// open. The row's stored URL is preferred while unexpired; otherwise the same
// provider resolver the serve path uses is asked for the row's candidate. The
// caller's existing probe budget bounds the resolve and the probe.
func (h *PlaybackHandler) siblingProbeURL(ctx context.Context, row *models.MediaFile) (string, map[string]string, bool) {
	if row == nil {
		return "", nil, false
	}
	now := time.Now()
	if url := strings.TrimSpace(row.ResolvedURL); url != "" &&
		(row.ResolvedURLExpiresAt == nil || row.ResolvedURLExpiresAt.After(now)) {
		return url, cloneHeaderMap(row.ProviderRequestHeaders), true
	}
	if h.VirtualMediaDetailedResolver != nil {
		resolved, err := h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(
			ctx, row.FilePath, row.VirtualOwnerInstallationID, 0, "", false, nil, "",
		)
		if err == nil && strings.TrimSpace(resolved.URL) != "" {
			return resolved.URL, cloneHeaderMap(resolved.RequestHeaders), true
		}
	}
	if h.VirtualPlaybackResolver != nil {
		url, err := h.VirtualPlaybackResolver.ResolveVirtualPlayback(
			ctx, row.FilePath, 0, "", row.VirtualOwnerInstallationID,
		)
		if err == nil && strings.TrimSpace(url) != "" {
			return url, nil, true
		}
	}
	return "", nil, false
}

// logVirtualSiblingProbeAdopted records that the size-mismatch fallback adopted
// a same-release sibling as the chain's effective row, so the verified
// inventory belongs to the sibling's bytes.
func logVirtualSiblingProbeAdopted(ctx context.Context, anchorFileID int, sibling *virtualSiblingProbe) {
	if sibling == nil {
		return
	}
	slog.WarnContext(ctx, "virtual probe served size mismatch: adopted same-release sibling",
		"component", "api", "file_id", anchorFileID, "sibling_file_id", catalogFileIDOrZero(sibling.row),
		"indexer", virtualCandidateIndexerLabel(sibling.cand), "sibling_uri", sibling.cand.URI)
}

// probeVirtualSourceAndPersist probes an already-resolved provider URL and
// persists the probed inventory back to the catalog row the client requested,
// so the next start of that row reuses the evidence instead of re-probing. It
// is the shared tail of the deferred start-path probe and the optimistic-start
// revalidation. bgCtx bounds the whole probe; the runtime-plausibility guard
// and the probe-failure damper are applied here so both callers behave
// identically.
//
// fence, when supplied, is re-checked after the blocking probe returns and
// before any failure verdict, unpin, or catalog write. A caller whose work was
// scheduled against a binding that another rotation superseded passes a fence
// that reports false once the binding moves, so the stale probe neither indicts
// the pin, releases it, nor persists evidence for a candidate the session no
// longer serves. The newer rotation's own refresh re-probes the live candidate.
func (h *PlaybackHandler) probeVirtualSourceAndPersist(
	bgCtx context.Context,
	stickyKey string,
	catalogFile *models.MediaFile,
	probeURL string,
	probeTransient models.MediaFile,
	probeCand VirtualPlaybackStream,
	candidate resolver.PersistedIdentityTiers,
	expectedRuntimeMinutes int,
	ownerInstallationID int,
	fence ...func() bool,
) string {
	var bindingFence func() bool
	if len(fence) > 0 {
		bindingFence = fence[0]
	}
	outcome, _ := h.probeVirtualSourceAndPersistWith(bgCtx, stickyKey, catalogFile, probeURL, probeTransient, probeCand, candidate, expectedRuntimeMinutes, ownerInstallationID, false, bindingFence)
	return outcome
}

// probeVirtualSourceAndPersistWith is probeVirtualSourceAndPersist with an
// explicit evidence-write mode. durableEvidence makes the catalog write
// synchronous and its result authoritative: the probe reports verified only when
// the write committed, and reports failed when the write is rejected or
// terminally fails. The deferred post-commit probe uses it so a session's
// outcome never claims a verified menu while the row the poll reads still shows
// the old one — the premature-verified case where a client stops loading on the
// old menu. Ordinary background callers pass false and keep the queued,
// coalescing write, where a positive verdict means "probe succeeded", not "row
// committed", exactly as before.
func (h *PlaybackHandler) probeVirtualSourceAndPersistWith(
	bgCtx context.Context,
	stickyKey string,
	catalogFile *models.MediaFile,
	probeURL string,
	probeTransient models.MediaFile,
	probeCand VirtualPlaybackStream,
	candidate resolver.PersistedIdentityTiers,
	expectedRuntimeMinutes int,
	ownerInstallationID int,
	durableEvidence bool,
	fence func() bool,
) (string, virtualCommittedProbeInventory) {
	probeKey := virtualProbeFailureKey(probeCand.URI, ownerInstallationID)
	// Carry the served candidate's declared audio onto the probe input before
	// the enumeration. The background/rotation paths build probeTransient from
	// the catalog row alone, so without this the completeness gate in
	// scanner.ProbeVirtualSource cannot see that the item carries audio and a
	// video-with-subtitles-but-no-audio result would be persisted and published
	// as verified. It is a no-op for the deferred and declared paths, whose
	// probe input the resolve already merged the candidate onto.
	forwardVirtualCandidateDeclaredAudio(&probeTransient, probeCand)
	// Carry the expected runtime on the probe input so the probe cache applies
	// the same size/duration identity rule it is stored under.
	probeTransient.VirtualExpectedRuntimeMinutes = expectedRuntimeMinutes
	probeCtx, probeCancel := context.WithTimeout(bgCtx, h.probeBackgroundBudget())
	probed, probeErr := h.probeVirtualSource(probeCtx, probeURL, &probeTransient, probeCand.RequestHeaders)
	probeCancel()
	// The probe (the last blocking call) can outlive the binding it was
	// scheduled for. Re-check the fence before this probe touches shared state:
	// a superseded verdict must not mark the damper, unpin the current
	// candidate, or persist evidence onto a row the session no longer serves.
	if fence != nil && !fence() {
		slog.InfoContext(bgCtx, "virtual probe evidence dropped: candidate binding moved during the probe",
			"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID)
		return "", virtualCommittedProbeInventory{}
	}
	if probeErr != nil || probed == nil {
		if errors.Is(probeErr, scanner.ErrVirtualProbePartial) {
			// The probe completed but enumerated only part of the stream
			// table. That is not a verdict about the candidate's bytes, so do
			// not mark the failure damper or release the pin. The negotiated
			// path holds the session pending (non-verified) and lets the
			// convergence watchdog fail it at the bound; the non-negotiated
			// background path has no session watchdog, so its bound is the
			// incomplete-enumeration streak: once it repeats, the failure
			// damper is armed so the resolve stops re-probing and settles on
			// the candidate's declared metadata instead of staying pending
			// forever. Nothing is persisted either way, so the row keeps its
			// previous evidence and stamp.
			if !durableEvidence &&
				virtualProbeFailures.bumpPartial(probeKey) >= virtualProbePartialRepeatThreshold {
				virtualProbeFailures.mark(probeKey)
				slog.WarnContext(bgCtx, "virtual probe enumeration still incomplete after repeated attempts; settling on declared metadata",
					"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID,
					"attempts", virtualProbePartialRepeatThreshold)
			}
			slog.WarnContext(bgCtx, "virtual probe enumeration incomplete; holding inventory pending",
				"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID)
			return probeOutcomePartial, virtualCommittedProbeInventory{}
		}
		// A context error (outer budget fired, request canceled) means the
		// verdict is unknown: the inner probe keeps its own timeout and may
		// still complete and land in the cache, so it must not arm the damper.
		// Arming it here is what let one canceled background probe serve an
		// empty declared inventory for every later start of the candidate.
		// A candidate-specific rejection is definitive; every other error must
		// repeat before the pin is released, so one slow provider cannot steer
		// later starts.
		if !virtualProbeVerdictUnknown(probeErr) {
			virtualProbeFailures.mark(probeKey)
		}
		slog.WarnContext(bgCtx, "background virtual stream probe failed", "component", "api", "candidate_uri", probeCand.URI, "error", probeErr)
		if errors.Is(probeErr, scanner.ErrVirtualProbeNoTracks) ||
			virtualProbeFailures.count(probeKey) >= virtualProbeFailureRepeatThreshold {
			h.unpinVirtualSticky(stickyKey, probeCand.URI)
		}
		return probeOutcomeFailed, virtualCommittedProbeInventory{}
	}
	anchorFileID := catalogFileIDOrZero(catalogFile)
	var adoptedSibling *virtualSiblingProbe
	// Identity is decided by what the probe measured, not by the declared
	// size: a served container that disagrees with the row's declared release
	// size stays usable but unverified, while a measured duration or track
	// layout that contradicts the declaration identifies a different file.
	evidencePin := scanner.VirtualServedSizePin(probeCand.URI, ownerInstallationID)
	evidenceVerdict := classifyVirtualServedEvidenceV3(evidencePin, probeTransient.FileSize, &probeTransient, probed, expectedRuntimeMinutes)
	sizeGap := scanner.VirtualServedSizeMismatch(probeTransient.FileSize, probed.ProbedFormatSize)
	if !virtualRuntimePlausible(probed.Duration, expectedRuntimeMinutes) && !sizeGap {
		// Matching declared size, impossible runtime: the historical duration
		// rejection and its dead-candidate damper stay as they were.
		virtualProbeFailures.mark(probeKey)
		slog.WarnContext(bgCtx, "background virtual probe rejected: probed duration implausible",
			"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID,
			"probed_duration_seconds", probed.Duration, "expected_runtime_minutes", expectedRuntimeMinutes)
		h.unpinVirtualSticky(stickyKey, probeCand.URI)
		return probeOutcomeFailed, virtualCommittedProbeInventory{}
	}
	if evidenceVerdict == scanner.VirtualServedEvidenceMismatch {
		// The container was readable but it is not the release the row
		// declares: the provider is serving a different file under this URL
		// (a stale cache fill, typically). Do not persist this inventory and do
		// not stamp the row failed — the release is not dead, the cache is stale
		// — so the row stays eligible and is re-probed on the short wrong-source
		// backoff. The probe input's declared size is the comparison basis
		// because it belongs to the row whose bytes were probed, including a
		// resolver-substituted candidate's own row.
		virtualProbeFailures.markSizeMismatch(probeKey, probed.ProbedFormatSize, probeTransient.FileSize)
		logVirtualProbeSizeMismatch(bgCtx, anchorFileID, probeCand.URI, probeTransient.FileSize, probed.ProbedFormatSize)
		h.unpinVirtualSticky(stickyKey, probeCand.URI)
		// The same release is usually offered by several indexers, each its own
		// candidate id and its own dice roll at the provider's cache. Probe the
		// release's sibling rows before giving up. A sibling that matches
		// re-points the chain at that row: the verified inventory below is the
		// sibling's bytes and is written to the sibling's row only, never the
		// selected row's. The selected row keeps the short damper armed above.
		sibling := h.matchSameReleaseSiblingProbe(bgCtx, catalogFile, probeCand, ownerInstallationID)
		if sibling == nil {
			return probeOutcomeFailed, virtualCommittedProbeInventory{}
		}
		catalogFile = sibling.row
		probeCand = sibling.cand
		probeTransient = sibling.transient
		probed = sibling.probed
		candidate = sibling.identity
		probeKey = virtualProbeFailureKey(probeCand.URI, probeCand.OwnerInstallationID)
		adoptedSibling = sibling
		// The chain now writes the sibling's bytes. A size-gap sibling is
		// usable but not confirmed, so its inventory is written metadata-only
		// and the pin stays unproven until a later serve lands on it.
		evidenceVerdict = scanner.VirtualServedEvidenceAdmitted
		if sibling.observation {
			evidenceVerdict = scanner.VirtualServedEvidenceUnverified
		}
	} else if sizeGap {
		// The posting disagrees with what the indexer advertised. The bytes are
		// still usable; the verdict records whether they establish identity.
		// The size disagreement stays logged so a posting discrepancy is still
		// visible to an operator.
		logVirtualProbeSizeGapV3(bgCtx, anchorFileID, probeCand.URI, probeTransient.FileSize, probed.ProbedFormatSize, probed.Duration, expectedRuntimeMinutes,
			virtualProbeEvidenceWithholdsIdentity(evidenceVerdict))
	}
	virtualProbeFailures.clear(probeKey)
	virtualProbeFailures.clearPartial(probeKey)
	virtualProbeFailures.clearSizeMismatch(probeKey)
	if probeTransient.ID > 0 {
		probed.ID = probeTransient.ID
		probed.MediaFolderID = probeTransient.MediaFolderID
	}
	if probeTransient.Duration > 0 && probed.Duration <= 0 {
		probed.Duration = probeTransient.Duration
	}
	mergeVirtualCandidateTracks(probed, probeCand)
	withholdsIdentity := virtualProbeEvidenceWithholdsIdentity(evidenceVerdict)
	if durableEvidence {
		// The outcome must not outrun the write: report verified only when the
		// durable write committed the enumerated tracks onto the row the
		// inventory poll reads. An unverified verdict still writes its measured
		// inventory, metadata-only, so the client gets real menus while the pin
		// stays unproven. A rejected or failed write is a terminal failed
		// outcome when the evidence is identity, so the client leaves loading
		// instead of showing a stale menu as verified. For a metadata-only
		// observation a refused write must not fail the play: the session is
		// served the carrier below.
		committedInv, committed := h.persistVirtualProbeEvidenceDurable(bgCtx, catalogFile, probeCand.URI, probed, candidate, !withholdsIdentity)
		if !committed {
			if withholdsIdentity {
				slog.WarnContext(bgCtx, "deferred virtual probe inventory write did not commit; serving the measured inventory for this session",
					"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID)
				return probeOutcomeVerified, probeInventoryCarrier(catalogFile, probeCand.URI, probed)
			}
			slog.WarnContext(bgCtx, "deferred virtual probe evidence write did not commit; reporting failed",
				"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID)
			return probeOutcomeFailed, virtualCommittedProbeInventory{}
		}
		logVirtualSiblingProbeAdopted(bgCtx, anchorFileID, adoptedSibling)
		// The durable write committed a verified inventory for a session whose
		// committed recipe was built from declared metadata, so a reorder can
		// leave the executable default-audio selection on the wrong language.
		// The caller runs the shared reconcileVerifiedDefaultAudio hook against
		// the committed owner row after it releases the shared gate, exactly as
		// the direct and queued evidence paths do, so a deferred session's
		// recipe keeps the preferred language.
		return probeOutcomeVerified, committedInv
	}
	// Write the measured inventory on every accepted verdict. An unverified
	// verdict writes metadata-only so the row reports its real tracks without
	// claiming the release identity.
	h.persistVirtualProbeEvidence(bgCtx, catalogFile, probeCand.URI, probed, candidate, !withholdsIdentity, false)
	logVirtualSiblingProbeAdopted(bgCtx, anchorFileID, adoptedSibling)
	return probeOutcomeVerified, virtualCommittedProbeInventory{}
}

// persistVirtualProbeEvidenceDurable performs one synchronous, durable evidence
// write for a caller whose outcome depends on the evidence being visible before
// the outcome is recorded. It exists for the deferred post-commit probe: that
// probe's terminal outcome is stored on the session and read by the inventory
// poll, so reporting verified while the row still shows its old menu would make
// a client stop loading on stale tracks. Visibility comes from ordering, not a
// read-back — the write commits synchronously before the outcome and publish, so
// a poll that follows the outcome reads the committed row. A queued write cannot
// give that guarantee — it may still be pending, or be rejected, or coalesce
// onto a later snapshot — so the durable path bypasses the buffer rather than
// guessing.
//
// The write prefers the explicit adoption-result saver so it can tell a genuine
// commit (MetadataUpdated) from a no-op CAS miss. The bare row-count saver only
// reports how many rows matched, so it is treated as committed when it affected
// at least one row and as not-committed otherwise — it cannot distinguish a CAS
// miss (superseded snapshot) from a plain no-op, but a zero-row write is never
// reported verified.
//
// On a commit it returns the exact inventory the write produced — the tracks,
// the row, the candidate URI and the release identity that was written — carried
// forward from the write arguments rather than reread from the row. The row is
// mutable, so a reread could observe an in-place adoption that replaced its
// tracks with another release's while the binding generation was unchanged; the
// caller propagates the carried inventory under its own generation fence and must
// never trust a reread.
func (h *PlaybackHandler) persistVirtualProbeEvidenceDurable(ctx context.Context, catalogFile *models.MediaFile, resolvedPath string, probed *models.MediaFile, candidate resolver.PersistedIdentityTiers, stampProbe bool) (virtualCommittedProbeInventory, bool) {
	// A detail read or a provider re-list can advance the row's revision while
	// the detached probe is running. Re-read the exact row immediately before
	// the write so the CAS snapshot and the adoption target are current; using
	// the pre-probe snapshot here made a successful probe look like a
	// superseded write and left the session fail-closed on its old inventory.
	catalogFile = h.refreshVirtualCatalogFileForEvidence(ctx, catalogFile)
	args, _, ok := h.virtualProbeEvidenceArgs(ctx, catalogFile, resolvedPath, probed, candidate, stampProbe)
	if !ok {
		slog.WarnContext(ctx, "durable virtual probe evidence write refused before execution",
			"component", "api", "file_id", catalogFileIDOrZero(catalogFile), "candidate_uri", resolvedPath)
		return virtualCommittedProbeInventory{}, false
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), virtualEvidencePersistBudget)
	defer cancel()
	if h.VirtualFileMetadataSaver != nil {
		result, err := h.VirtualFileMetadataSaver(writeCtx, args)
		if err != nil {
			slog.WarnContext(ctx, "durable virtual probe evidence write failed",
				"component", "api", "file_id", args.FileID, "error", err)
			return virtualCommittedProbeInventory{}, false
		}
		if result.MetadataUpdated {
			return committedProbeInventoryFromWrite(args, resolvedPath, candidate, probed), true
		}
		// A CAS miss is the expected outcome when a newer writer already
		// committed the row; the evidence this probe enumerated is then
		// superseded and must not be reported verified.
		slog.WarnContext(ctx, "durable virtual probe evidence write matched no row (superseded snapshot)",
			"component", "api", "file_id", args.FileID, "candidate_uri", resolvedPath)
		return virtualCommittedProbeInventory{}, false
	}
	if h.VirtualFileSaver == nil {
		return virtualCommittedProbeInventory{}, false
	}
	rows, err := h.VirtualFileSaver(writeCtx, args)
	if err != nil {
		slog.WarnContext(ctx, "durable virtual probe evidence write failed",
			"component", "api", "file_id", args.FileID, "error", err)
		return virtualCommittedProbeInventory{}, false
	}
	if rows <= 0 {
		return virtualCommittedProbeInventory{}, false
	}
	return committedProbeInventoryFromWrite(args, resolvedPath, candidate, probed), true
}

// committedProbeInventoryFromWrite captures the exact inventory a successful
// durable evidence write committed, from the write arguments and the probed
// evidence rather than a reread of the row. The release identity is the row's
// stored identity when the write was metadata-only (the row keeps what it had)
// and the resolved candidate's identity when the write required an adoption that
// replaces the stored transport set as a unit.
func committedProbeInventoryFromWrite(args models.VirtualFilePersistArgs, resolvedPath string, candidate resolver.PersistedIdentityTiers, probed *models.MediaFile) virtualCommittedProbeInventory {
	identity := resolver.NewPersistedIdentityTiers(args.ExpectedProviderVideoHash, args.ExpectedProviderGUID, args.ExpectedProviderReleaseName, args.ExpectedProviderReleaseSize)
	if args.RequireAdopt && args.AdoptPath != "" {
		identity = candidate
	}
	inv := virtualCommittedProbeInventory{
		fileID:       args.FileID,
		candidateURI: resolvedPath,
		identity:     identity,
		written:      true,
	}
	if probed != nil {
		inv.audio = append([]models.AudioTrack(nil), probed.AudioTracks...)
		inv.subtitles = append([]models.SubtitleTrack(nil), probed.SubtitleTracks...)
		inv.external = append([]models.ExternalSubtitle(nil), probed.ExternalSubtitles...)
	}
	return inv
}

// probeInventoryCarrier names the inventory a probe measured for a session
// without a catalog write. The one caller is a serve that records its pin's
// first served-size observation: the session menus must show the tracks the
// bytes actually carry, but the row may not be stamped yet (see
// virtualProbeEvidenceWithholdsIdentity), so there is no committed write to carry.
//
// The release identity is the row's stored identity, because that is the
// identity the untouched row keeps, exactly like the metadata-only write the
// persist path would have produced. It is deliberately not the resolved
// candidate's identity: nothing was adopted.
func probeInventoryCarrier(file *models.MediaFile, candidateURI string, probed *models.MediaFile) virtualCommittedProbeInventory {
	inv := virtualCommittedProbeInventory{
		fileID:       catalogFileIDOrZero(file),
		candidateURI: candidateURI,
	}
	if file != nil {
		inv.identity = resolver.NewPersistedIdentityTiers(
			file.ProviderVideoHash, file.ProviderGUID, file.ProviderReleaseName, file.ProviderReleaseSize,
		)
	}
	if probed != nil {
		inv.audio = append([]models.AudioTrack(nil), probed.AudioTracks...)
		inv.subtitles = append([]models.SubtitleTrack(nil), probed.SubtitleTracks...)
		inv.external = append([]models.ExternalSubtitle(nil), probed.ExternalSubtitles...)
	}
	return inv
}

// catalogFileIDOrZero is a nil-safe file id for a log attribute.
func catalogFileIDOrZero(file *models.MediaFile) int {
	if file == nil {
		return 0
	}
	return file.ID
}

// virtualProbeFromCache returns a probe already completed for this candidate's
// source bytes, or nil when the cache holds no entry. It is the cache-first seam
// shared by the synchronous resolve and every recovery path: the virtual probe
// cache is keyed on the canonical candidate URI and a query-stripped provider
// URL, so a credential rotation on the same provider path still hits. Serving
// the cached evidence skips a redundant ffprobe — the dominant cost of an
// audio-switch or version-switch replan — and persists it with a fresh stamp so
// the row stops being probed on every start. A hit the pin-aware gate only
// observes still writes its measured inventory metadata-only, but is not
// stamped or pinned (see virtualProbeEvidenceWithholdsIdentity). The failure
// damper is cleared on a hit, exactly like the transient-timeout recovery it
// generalizes.
//
// The second return value reports whether that gate classed the hit as the
// pin's first served-size observation rather than its identity. Callers must
// rely on it and never re-classify the served bytes themselves: the
// classification records that observation as a side effect, so a second pass
// would compare the hit against the snapshot the first pass just wrote, read
// its own recording as a confirming serve, and admit bytes nothing confirmed.
//
// The candidate's declared audio is forwarded onto the lookup input before the
// cache is consulted. The cache key folds in the completeness gate's
// expected-audio signal (see virtualProbeCacheKey), and the caller's probe input
// is often the catalog row or a thin transient built before the served
// candidate's declared metadata was merged — so a row that carries no audio
// metadata would otherwise look up the video-only key, retrieve a thin
// video-without-audio enumeration cached under that expectation, and the caller
// would merge synthesized candidate audio onto it and report it verified. Doing
// the forward here, at the one seam every cache-first caller shares, is what
// makes the key partitioning effective: the lookup key then matches the
// expectation the real probe would have used.
func (h *PlaybackHandler) virtualProbeFromCache(
	ctx context.Context,
	catalogFile *models.MediaFile,
	sourceURL string,
	probeFile models.MediaFile,
	probeCand VirtualPlaybackStream,
	candidate resolver.PersistedIdentityTiers,
	ownerInstallationID int,
) (*models.MediaFile, bool) {
	if h == nil || h.VirtualProbeCacheLookup == nil || catalogFile == nil {
		return nil, false
	}
	forwardVirtualCandidateDeclaredAudio(&probeFile, probeCand)
	probed := h.VirtualProbeCacheLookup(sourceURL, &probeFile)
	if probed == nil {
		return nil, false
	}
	expectedRuntimeMinutes := h.virtualExpectedRuntimeMinutes(ctx, catalogFile)
	evidencePin := scanner.VirtualServedSizePin(probeCand.URI, ownerInstallationID)
	evidenceVerdict := classifyVirtualServedEvidenceV3(evidencePin, probeFile.FileSize, &probeFile, probed, expectedRuntimeMinutes)
	if evidenceVerdict == scanner.VirtualServedEvidenceMismatch {
		// The cached evidence measured a different release's bytes. Replaying
		// it would persist exactly the inventory the probe persist path
		// refuses, so the hit is discarded and the row is left unprobed. The
		// short wrong-source damper keeps the cache from being replayed on
		// every start until the provider heals. A size mismatch whose duration
		// proves the content is a short posting, not a wrong file, and is used
		// normally below.
		virtualProbeFailures.markSizeMismatch(virtualProbeFailureKey(probeCand.URI, ownerInstallationID), probed.ProbedFormatSize, probeFile.FileSize)
		logVirtualProbeSizeMismatch(ctx, catalogFile.ID, probeCand.URI, probeFile.FileSize, probed.ProbedFormatSize)
		return nil, false
	}
	if scanner.VirtualServedSizeMismatch(probeFile.FileSize, probed.ProbedFormatSize) {
		logVirtualProbeSizeGapV3(ctx, catalogFile.ID, probeCand.URI, probeFile.FileSize, probed.ProbedFormatSize, probed.Duration, expectedRuntimeMinutes,
			virtualProbeEvidenceWithholdsIdentity(evidenceVerdict))
	}
	virtualProbeFailures.clear(virtualProbeFailureKey(probeCand.URI, ownerInstallationID))
	virtualProbeFailures.clearSizeMismatch(virtualProbeFailureKey(probeCand.URI, ownerInstallationID))
	if probeFile.ID > 0 {
		probed.ID = probeFile.ID
		probed.MediaFolderID = probeFile.MediaFolderID
	}
	if probeFile.Duration > 0 && probed.Duration <= 0 {
		probed.Duration = probeFile.Duration
	}
	mergeVirtualCandidateTracks(probed, probeCand)
	observation := virtualProbeEvidenceWithholdsIdentity(evidenceVerdict)
	// Write the inventory on both dispositions. An admitted hit gets the fresh
	// stamp; an observation is written metadata-only so the row reports its
	// real tracks while the next replay still probes and confirms the pin.
	h.persistVirtualProbeEvidence(ctx, catalogFile, probeCand.URI, probed, candidate, !observation, false)
	return probed, observation
}

// recoverVirtualProbeFromCache attempts a cache-only probe for a candidate the
// failure damper would otherwise skip. A transient outer timeout can leave the
// inner probe running under its own scanner.VirtualProbeTimeout; when it
// completes it lands in the probe cache, so the completed evidence is available
// without paying the provider round-trip again. Recovering it persists the
// evidence and clears the damper, so a transient timeout does not starve the
// row until the backoff lapses. It reports whether cached evidence was used.
func (h *PlaybackHandler) recoverVirtualProbeFromCache(
	ctx context.Context,
	catalogFile *models.MediaFile,
	sourceURL string,
	probeFile models.MediaFile,
	probeCand VirtualPlaybackStream,
	candidate resolver.PersistedIdentityTiers,
	ownerInstallationID int,
) bool {
	probed, _ := h.virtualProbeFromCache(ctx, catalogFile, sourceURL, probeFile, probeCand, candidate, ownerInstallationID)
	return probed != nil
}

// revalidateVirtualCandidateBackground resolves the provider URL for a
// candidate the start path returned optimistically and runs the probe+persist
// chain in the background. The start response may already be sent, so the
// whole chain keeps a WithoutCancel context; a failed revalidation only marks
// the probe damper and clears the sticky pin so the next start re-ranks.
func (h *PlaybackHandler) revalidateVirtualCandidateBackground(
	requestCtx context.Context,
	stickyKey string,
	file *models.MediaFile,
	cand VirtualPlaybackStream,
	oid int,
	userID int,
	profileID string,
	targetID int,
) {
	gate := h.detachedGate()
	if !gate.tryAcquire() {
		slog.WarnContext(requestCtx, "optimistic virtual revalidation skipped: detached worker budget exhausted",
			"component", "api", "candidate_uri", cand.URI)
		return
	}
	go func() {
		defer gate.release()
		// The optimistic start may already have been sent, so the context
		// drops the request cancellation but still follows the service
		// lifecycle; it carries its own startup budget.
		bgCtx, bgCancel := h.virtualDetachedContext(requestCtx, virtualStartupBudget)
		defer bgCancel()
		// This revalidates the specific candidate the optimistic start is
		// already serving, so it is session-bound: a profile-removed candidate
		// is reported as a resolve failure (damper + unpin) instead of being
		// silently substituted by a different release.
		bgCtx = withVirtualSessionBindingV3(bgCtx, true)

		var streamURL string
		var resolveErr error
		var resolvedIdentity virtualProbeIdentity
		if h.VirtualMediaDetailedResolver != nil {
			res, err := h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(
				bgCtx, cand.URI, oid, userID, profileID, false, nil, "",
			)
			if err != nil {
				resolveErr = err
			} else {
				streamURL = res.URL
				cand.RequestHeaders = cloneHeaderMap(res.RequestHeaders)
				resolvedIdentity = virtualProbeIdentity{
					VideoHash:   res.ProviderVideoHash,
					GUID:        res.ProviderGUID,
					ReleaseName: res.ProviderReleaseName,
					ReleaseSize: res.ProviderReleaseSize,
				}
				if res.URI != "" {
					cand.URI = res.URI
				}
				if res.CandidateID != "" {
					cand.ID = res.CandidateID
				}
			}
		} else if h.VirtualPlaybackResolver != nil {
			streamURL, resolveErr = h.VirtualPlaybackResolver.ResolveVirtualPlayback(
				bgCtx, cand.URI, userID, profileID, oid,
			)
			cand.RequestHeaders = nil
		} else {
			return
		}
		if resolveErr != nil {
			resolveKey := virtualProbeFailureKey(cand.URI, oid)
			virtualProbeFailures.mark(resolveKey)
			slog.WarnContext(bgCtx, "optimistic virtual revalidation resolve failed", "component", "api", "candidate_uri", cand.URI, "error", resolveErr)
			// A transient RPC timeout leaves the candidate's health unknown;
			// only a repeat (or a concrete provider rejection) releases it.
			if !virtualProbeVerdictUnknown(resolveErr) ||
				virtualProbeFailures.count(resolveKey) >= virtualProbeFailureRepeatThreshold {
				h.unpinVirtualSticky(stickyKey, cand.URI)
			}
			return
		}

		probeTransient := cloneVirtualProbeTransient(*file)
		var dbFile *models.MediaFile
		if h.VirtualFileLookup != nil {
			var lookupErr error
			dbFile, lookupErr = h.VirtualFileLookup(bgCtx, cand.URI)
			if lookupErr != nil && !isVirtualCandidateNotFound(lookupErr) {
				slog.WarnContext(bgCtx, "optimistic virtual revalidation lookup failed", "component", "api", "error", lookupErr)
				return
			}
		}
		if (dbFile == nil || dbFile.ID <= 0) && h.VirtualCandidateFileLookup != nil {
			var lookupErr error
			dbFile, lookupErr = h.VirtualCandidateFileLookup(bgCtx, virtualPlaybackNeutralKey(cand.URI), file.ContentID, file.EpisodeID, oid)
			if lookupErr != nil && !isVirtualCandidateNotFound(lookupErr) {
				slog.WarnContext(bgCtx, "optimistic virtual candidate lookup failed", "component", "api", "error", lookupErr)
				return
			}
		}
		// Only a row whose path verifiably matches the candidate may be used
		// as the probe target. A partial row (ID with a null or stale
		// file_path) never matched the URI, so it must not receive the
		// resolved candidate's probe evidence.
		if dbFile != nil && virtualCandidateRowVerified(dbFile, cand.URI) {
			probeTransient = cloneVirtualProbeTransient(*dbFile)
		}
		probeTransient.FilePath = cand.URI
		probeTransient.VirtualOwnerInstallationID = oid
		if virtualProbeBackedOff(virtualProbeFailureKey(cand.URI, oid)) {
			// The damper would skip the probe, but a completed inner probe may
			// already be cached. Recover it so the row is not left unprobed.
			h.recoverVirtualProbeFromCache(bgCtx, file, streamURL, probeTransient, cand, resolvedIdentity.tiers(), oid)
			return
		}
		h.probeVirtualSourceAndPersist(bgCtx, stickyKey, file, streamURL, probeTransient, cand, resolvedIdentity.tiers(), h.virtualExpectedRuntimeMinutes(bgCtx, file), oid)
	}()
}

// Refusal reasons for a required identity adoption that matched no row. The SQL
// fence cannot report which predicate refused, so the caller classifies the
// candidate against the fence's own precedence and carries the concrete cause
// in VirtualFilePersistArgs.RefusalReason; the saver logs it instead of one
// lumped bucket. Keeping them as named constants means the persist path and the
// diagnosis cannot drift.
const (
	// virtualProbeRefusalSiblingOwner: another row of the same virtual owner and
	// library already owns the candidate's concrete path.
	virtualProbeRefusalSiblingOwner = "sibling_owner"
	// virtualProbeRefusalFailedVerdict: the candidate identity carries a live
	// failed_at verdict, so adoption is fenced even without a sibling owner.
	virtualProbeRefusalFailedVerdict = "failed_verdict"
	// virtualProbeRefusalCollectionRow: the row is collection-owned, so its path
	// is owned by the collection sync and can never be adopted.
	virtualProbeRefusalCollectionRow = "collection_row"
	// virtualProbeRefusalCrossReleaseNoTarget: a cross-release candidate with no
	// adoption target at all (a collection-owned row) is refused before enqueue.
	virtualProbeRefusalCrossReleaseNoTarget = "cross_release_without_adopt_target"
	// virtualProbeRefusalOwnerLookupFailed: the identity guard could not answer
	// who owns the candidate path, so the write fails closed.
	virtualProbeRefusalOwnerLookupFailed = "owner_lookup_failed"
	// virtualProbeRefusalStaleSnapshot: none of the deterministic causes above
	// applies, so the CAS snapshot no longer matches the row (a newer writer
	// already committed it).
	virtualProbeRefusalStaleSnapshot = "stale_snapshot"
	// virtualProbeRefusalUnclassified is the neutral fallback for a required
	// adoption the caller did not classify. It is deliberately not a claim about
	// which predicate refused.
	virtualProbeRefusalUnclassified = "adoption_fence"
)

// VirtualFileMetadataUpdateSQL persists a probed virtual inventory back to
// media_files. It also stamps probe_source/probe_updated_at so the playback
// probe gate can recognize the row as really probed and stop re-probing it on
// every start. probe_source stays 'virtual_collection' on collection-owned
// rows so the collection materializer keeps recognizing them; a real playback
// probe (stampProbe=true) still stamps probe_updated_at so collection rows
// converge to probed evidence instead of re-probing on every start.
//
// Path adoption is skipped when a sibling row (same virtual owner and library)
// already owns the target path, and when the candidate's own verdict is failed.
// When RequireAdopt is requested, the whole write is atomic with that fence: the
// $22 guard mirrors the file_path CASE in the UPDATE's WHERE, so a refused
// adoption (sibling owner, collection row, live failed verdict) matches no row
// and leaves the track inventory and probe stamp untouched. Metadata-only
// writers leave $22 false and keep the previous behavior: adoption is best-effort
// while the metadata and stamp still apply. The one exception is a
// provider-neutral row writing a candidate URI a sibling row already owns: the
// top-level candidate-ownership fence refuses the whole write so its tracks and
// stamp cannot describe bytes the row does not own.
//
// Adoption is additionally fenced on the candidate's own verdict. A failed_at
// stamp committed after the handler's last verdict read (the serve layer and
// another replan both write one) must still prevent adoption, so the same
// statement that writes the row re-checks for a live failed verdict on the
// validated identity ($18 exact, $19 provider-neutral when no exact row owns
// the path). The predicate matches virtualCandidateVerdictActive: a stamp is
// live while now() <= failed_at + $20 seconds. $21 disables the fence for an
// explicit retry (AllowFailedVerdict); it is otherwise set from RequireAdopt so
// metadata-only writers keep their previous unconditional adoption. The
// statement returns the persisted file_path so the saver can confirm the
// validated identity actually landed rather than trusting a positive row count
// (see RequireAdopt).
const VirtualFileMetadataUpdateSQL = `
UPDATE media_files SET
  video_tracks     = $1::jsonb,
  audio_tracks     = $2::jsonb,
  subtitle_tracks  = $3::jsonb,
  resolution       = NULLIF($4,''),
  codec_video      = NULLIF($5,''),
  codec_audio      = NULLIF($6,''),
  container        = NULLIF($7,''),
  hdr              = $8,
  bitrate          = NULLIF($9,0),
  -- The persisted provider URL and its durable identity are additive to the
  -- neutral ?result= file_path. A resolution with no URL (a metadata-only
  -- write) must not erase the stored one; only a newer successful resolution
  -- overwrites it, and its expiry is replaced with it so the pair always
  -- describes the same URL.
  --
  -- A required adoption of a different release ($30) replaces the whole
  -- transport set instead of preserving on omission: an omitted identity tier,
  -- URL, expiry or header set belongs to the release being left behind and must
  -- not survive attached to the new release path. For the same release a
  -- supplied URL still replaces the URL and its header set (a refreshed URL
  -- with no headers clears the old set rather than orphaning it), while an
  -- omitted tier preserves the last known value.
  resolved_url     = CASE
    WHEN $30 OR NULLIF($23,'') IS NOT NULL THEN NULLIF($23,'')
    ELSE resolved_url
  END,
  resolved_url_expires_at = CASE
    WHEN $30 OR NULLIF($23,'') IS NOT NULL THEN $24::timestamptz
    ELSE resolved_url_expires_at
  END,
  provider_video_hash     = CASE WHEN $30 THEN NULLIF($25,'') ELSE COALESCE(NULLIF($25,''), provider_video_hash) END,
  provider_guid           = CASE WHEN $30 THEN NULLIF($26,'') ELSE COALESCE(NULLIF($26,''), provider_guid) END,
  provider_release_name   = CASE WHEN $30 THEN NULLIF($27,'') ELSE COALESCE(NULLIF($27,''), provider_release_name) END,
  provider_release_size   = CASE WHEN $30 THEN NULLIF($28::bigint,0) ELSE COALESCE(NULLIF($28::bigint,0), provider_release_size) END,
  -- Request headers belong to the resolved URL. A transport replacement carries
  -- the complete new set (NULL when the new resolution has none, which clears
  -- the old one); without one, a metadata-only write preserves the stored set.
  provider_request_headers = CASE
    WHEN $30 OR NULLIF($23,'') IS NOT NULL THEN $29::jsonb
    ELSE COALESCE($29::jsonb, provider_request_headers)
  END,
  duration         = CASE WHEN $10 > 0 THEN $10 ELSE duration END,
  audio_channels   = COALESCE(
    (SELECT (elem->>'channels')::int
     FROM jsonb_array_elements(
       CASE WHEN jsonb_typeof($2::jsonb) = 'array' THEN $2::jsonb ELSE '[]'::jsonb END
     ) elem LIMIT 1),
    audio_channels
  ),
  file_path        = CASE
    -- The final boolean in this guard is the explicit collection-variant
    -- reconcile verdict: a collection path is otherwise never rewritten (the
    -- collection sync owns it), but a caller that proved the pinned release
    -- vanished from the fresh listing may re-point the row at the live
    -- candidate selected for its profile.
    WHEN $18 != '' AND (probe_source IS DISTINCT FROM 'virtual_collection' OR $32::boolean)
         AND NOT EXISTS (
           SELECT 1 FROM media_files sibling
           WHERE sibling.id <> media_files.id
             AND sibling.file_path = $18
             AND sibling.virtual_owner_installation_id IS NOT DISTINCT FROM $16
             AND sibling.media_folder_id IS NOT DISTINCT FROM $17
         )
         AND (
           NOT $21::boolean
           OR NOT EXISTS (
             SELECT 1 FROM media_files failed
             WHERE failed.virtual_owner_installation_id IS NOT DISTINCT FROM $16
               AND failed.media_folder_id IS NOT DISTINCT FROM $17
               AND failed.failed_at IS NOT NULL
               AND now() <= failed.failed_at + make_interval(secs => $20)
               AND (
                 failed.file_path = $18
                 OR (
                   $19 <> $18
                   AND failed.file_path = $19
                   AND NOT EXISTS (
                     SELECT 1 FROM media_files exact_row
                     WHERE exact_row.file_path = $18
                       AND exact_row.virtual_owner_installation_id IS NOT DISTINCT FROM $16
                       AND exact_row.media_folder_id IS NOT DISTINCT FROM $17
                   )
                 )
               )
           )
         )
    THEN $18
    ELSE file_path
  END,
  -- A clear invalidates evidence the caller proved does not describe the
  -- adopted bytes (a release swap): probe_source and probe_updated_at go
  -- together so the row no longer looks probed and the next start re-probes.
  -- Collection-owned rows keep their stamp, exactly like the stamp flag.
  probe_source     = CASE
    WHEN probe_source = 'virtual_collection' THEN probe_source
    WHEN $31::boolean THEN NULL
    WHEN NOT $13::boolean THEN probe_source
    ELSE 'virtual'
  END,
  -- The stamp is controlled by explicit successful-probe provenance: the
  -- caller sets the stamp flag only when the evidence it writes came from a
  -- probe that succeeded (a fresh probe, a cache hit, or the durable deferred
  -- write). It is never reconstructed from inventory shape, so a metadata-only
  -- write (a stored-URL refresh, a rematch adoption) leaves the flag false and
  -- preserves the existing stamp rather than advancing it from tracks it merely
  -- copied. ClearProbe still clears, and a collection-owned row still preserves
  -- its own stamp.
  probe_updated_at = CASE
    WHEN probe_source = 'virtual_collection' THEN probe_updated_at
    WHEN $31::boolean THEN NULL
    WHEN NOT $13::boolean THEN probe_updated_at
    ELSE GREATEST(clock_timestamp(), probe_updated_at + interval '1 microsecond')
  END,
  updated_at       = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = $11
  AND (NULLIF($12, '') IS NULL OR file_path = $12)
  AND updated_at     = $14
  AND probe_updated_at IS NOT DISTINCT FROM $15::timestamptz
  AND virtual_owner_installation_id IS NOT DISTINCT FROM $16
  AND media_folder_id IS NOT DISTINCT FROM $17
  -- A provider-neutral row (the row file_path equals the neutral key of the
  -- candidate URI) must not absorb evidence for a candidate a sibling row already
  -- owns: the tracks and stamp would describe bytes this row does not own. The
  -- file_path CASE below would skip the adoption, but a metadata-only write
  -- would still land; fence the whole neutral-row write on the candidate URI
  -- as well as the expected path. The comparison is against the actual row
  -- file_path rather than the optional expected path, which is empty for a
  -- write that does not constrain the row identity and would otherwise bypass
  -- the fence. Non-neutral writes are unchanged.
  AND (
    NULLIF($18, '') IS NULL
    OR NULLIF($19, '') IS NULL
    OR media_files.file_path IS DISTINCT FROM $19
    OR NOT EXISTS (
      SELECT 1 FROM media_files neutral_owner
      WHERE neutral_owner.id <> media_files.id
        AND neutral_owner.file_path = $18
        AND neutral_owner.virtual_owner_installation_id IS NOT DISTINCT FROM $16
        AND neutral_owner.media_folder_id IS NOT DISTINCT FROM $17
    )
  )
  -- Mirrors the file_path CASE below: when a confirmed adoption is required,
  -- the row only matches if that adoption will actually happen, so the track
  -- inventory and probe stamp cannot land without the identity. Keep the two
  -- predicates in sync.
  AND (
    NOT $22::boolean
    OR (
      $18 <> ''
      AND (probe_source IS DISTINCT FROM 'virtual_collection' OR $32::boolean)
      AND NOT EXISTS (
        SELECT 1 FROM media_files guard_sibling
        WHERE guard_sibling.id <> media_files.id
          AND guard_sibling.file_path = $18
          AND guard_sibling.virtual_owner_installation_id IS NOT DISTINCT FROM $16
          AND guard_sibling.media_folder_id IS NOT DISTINCT FROM $17
      )
      AND (
        NOT $21::boolean
        OR NOT EXISTS (
          SELECT 1 FROM media_files guard_failed
          WHERE guard_failed.virtual_owner_installation_id IS NOT DISTINCT FROM $16
            AND guard_failed.media_folder_id IS NOT DISTINCT FROM $17
            AND guard_failed.failed_at IS NOT NULL
            AND now() <= guard_failed.failed_at + make_interval(secs => $20)
            AND (
              guard_failed.file_path = $18
              OR (
                $19 <> $18
                AND guard_failed.file_path = $19
                AND NOT EXISTS (
                  SELECT 1 FROM media_files guard_exact
                  WHERE guard_exact.file_path = $18
                    AND guard_exact.virtual_owner_installation_id IS NOT DISTINCT FROM $16
                    AND guard_exact.media_folder_id IS NOT DISTINCT FROM $17
                )
              )
            )
        )
      )
    )
  )
RETURNING file_path
`

// VirtualFileMetadataDB is the minimal database surface the shared virtual
// metadata update needs. Both the native router wiring and the jellycompat
// wiring pass a *pgxpool.Pool, which satisfies this interface. RETURNING is
// used rather than Exec so the saver can confirm the identity actually adopted.
type VirtualFileMetadataDB interface {
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// VirtualFileMetadataTxBeginner is the transaction surface a real database
// exposes so the evidence write and the exact-pin refusal reconciliation commit
// atomically. *pgxpool.Pool satisfies it; the in-memory and fake databases of
// the unit tests deliberately do not, and fall back to the best-effort path.
type VirtualFileMetadataTxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// VirtualAdoptionRefusalTxStore is the transaction-aware refusal store: the
// durable Postgres store records, clears and terminally stamps refusal state on
// the caller's transaction, so the counter, the terminal verdict and the
// evidence write they belong to all commit or roll back together. A store that
// does not implement it keeps the previous best-effort path.
type VirtualAdoptionRefusalTxStore interface {
	// RecordVirtualAdoptionRefusalTx atomically increments the live consecutive
	// count for one row and pinned path on the transaction and returns it.
	RecordVirtualAdoptionRefusalTx(ctx context.Context, tx pgx.Tx, fileID int, expectedPath string, ttl time.Duration) (int, error)
	// ClearVirtualAdoptionRefusalTx drops the exact-pin mark on the transaction
	// after a write landed, so the clear commits with that write.
	ClearVirtualAdoptionRefusalTx(ctx context.Context, tx pgx.Tx, fileID int, expectedPath string) error
	// MarkVirtualAdoptionTerminalRefusalTx stamps the durable terminal verdict
	// on the transaction, fenced on the candidate identity and the observed
	// evidence revision. It reports whether the stamp landed.
	MarkVirtualAdoptionTerminalRefusalTx(ctx context.Context, tx pgx.Tx, fileID int, expectedFilePath string, observedFailedAt *time.Time, observedUpdatedAt time.Time, observedProbeUpdatedAt *time.Time) (bool, error)
}

// VirtualAdoptionRefusalSweeper is the optional post-commit expiry sweep. The
// non-transactional refusal record sweeps inline; the transactional path runs
// this after the transaction commits so the sweep never holds a second pooled
// connection while the transaction is open.
type VirtualAdoptionRefusalSweeper interface {
	SweepVirtualAdoptionRefusals(ctx context.Context) error
}

// virtualPersistAdoptionReplacesTransport reports whether a required adoption
// moves the row to a different release, in which case the stored transport set
// (resolved URL and expiry, durable identity tiers, request headers) must be
// replaced rather than preserved on omission.
//
// The test is the durable candidate identity in the same tier precedence the
// deduplication chain uses (video hash, then source GUID, then normalized
// release name plus exact size). It deliberately is not the item-level neutral
// key: virtual://movie/tt100?result=a and ?result=b share a neutral key, yet
// they are different releases unless their identity tiers prove otherwise. A
// tier present on only one side is not proof, so an unprovable pair replaces:
// preserving release A's hash under release B's path is exactly the defect this
// guards against.
func virtualPersistAdoptionReplacesTransport(args models.VirtualFilePersistArgs, adoptPath string) bool {
	// Adopting the path the row already carries changes no release: it is a
	// metadata-only write that happens to pass the same AdoptPath, and the
	// stored transport must survive. This also keeps a neutral row's own
	// candidate pick (a path with a ?result= added to the row's own neutral
	// path) on preserve-on-omission when neither side carries an identity.
	if strings.TrimSpace(adoptPath) == strings.TrimSpace(args.ExpectedFilePath) {
		return false
	}
	expected := resolver.PersistedDedupKey(
		args.ExpectedProviderVideoHash, args.ExpectedProviderGUID,
		args.ExpectedProviderReleaseName, args.ExpectedProviderReleaseSize,
	)
	candidate := resolver.PersistedDedupKey(
		args.ProviderVideoHash, args.ProviderGUID,
		args.ProviderReleaseName, args.ProviderReleaseSize,
	)
	if expected == "" || candidate == "" {
		return true
	}
	return expected != candidate
}

// VirtualFileMetadataUpdateResult reports metadata persistence separately
// from identity adoption. The UPDATE returns the persisted file_path, so
// adoption is observed atomically in the same statement: it holds exactly when
// a non-empty AdoptPath was requested and the row carries it afterwards. A
// successful UPDATE is not adoption when the row is collection-owned or the
// sibling guard retained its existing path, and callers must not infer adoption
// from row counts.
type VirtualFileMetadataUpdateResult struct {
	RowsAffected    int64
	MetadataUpdated bool
	IdentityAdopted bool
}

// ExecVirtualFileMetadataUpdateResult executes VirtualFileMetadataUpdateSQL
// and owns the adoption-race retry shared by the native and jellycompat
// savers.
//
// Two things are confirmed from the single statement that writes the row:
//
//   - The SQL's sibling guard keeps adoption from colliding with an existing
//     owner of the target path, and its verdict guard rejects adoption while
//     the validated candidate carries a live failed_at stamp. Because that
//     predicate is evaluated in the same statement as the write, a verdict
//     committed after the caller's last read but before this write cannot be
//     adopted.
//   - When RequireAdopt is set the whole write is atomic with that fence: a
//     refused adoption (probe_source guard, sibling guard, verdict fence, or a
//     collection row) matches no row, so the track inventory and probe stamp
//     are not written either. Metadata-only writers keep the previous
//     best-effort adoption. RETURNING file_path reports what the row actually
//     persisted when a row did match.
//   - A required adoption of a different release replaces the stored transport
//     fields (resolved URL and expiry, durable identity tiers, request headers)
//     as a set rather than preserving on omission: an omitted tier or URL
//     belongs to the release being left behind and must not survive attached to
//     the new release's path. "Different release" is decided by the durable
//     candidate identity (see virtualPersistAdoptionReplacesTransport), not the
//     item-level neutral key, so ?result=a -> ?result=b on one item replaces.
//     A proven same-release write keeps preserve-on-omission, while a supplied
//     URL always replaces the URL and its header set.
//   - ClearProbe invalidates the stored probe evidence in the same statement
//     (probe_source and probe_updated_at to NULL) so a row whose inventory no
//     longer describes the adopted bytes re-probes on the next start. It is
//     independent of StampProbe.
//
// Two concurrent probes can both pass the sibling guard and one still loses the
// unique-index race (media_files_virtual_file_owner_key). For a metadata-only
// write that happens with a non-empty AdoptPath: retry exactly once with
// AdoptPath cleared so the probe metadata lands on the row's current path
// instead of being dropped. The retry keys on SQLSTATE 23505 rather than the
// constraint name. When RequireAdopt is set there is no retry at all: a
// collision proves the identity was not adopted, and a metadata-only retry
// would stamp the substitute's tracks on a row that does not own them. A nil db
// is a no-op so callers that run without a database stay safe.
//
// QueryRow consumes the full result stream before Scan returns, so a terminal
// database error cannot hide behind an already-read row.
func ExecVirtualFileMetadataUpdateResult(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
	return execVirtualFileMetadataUpdateResult(ctx, db, args, virtualAdoptionRefusals)
}

// ExecVirtualFileMetadataUpdateResultWithRefusals is
// ExecVirtualFileMetadataUpdateResult with an explicit durable refusal store, so
// production wiring records the consecutive refusal through the cluster-wide
// store instead of the in-memory fallback.
func ExecVirtualFileMetadataUpdateResultWithRefusals(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs, store VirtualAdoptionRefusalStore) (VirtualFileMetadataUpdateResult, error) {
	return execVirtualFileMetadataUpdateResult(ctx, db, args, store)
}

// execVirtualFileMetadataUpdateResult is the shared implementation behind
// ExecVirtualFileMetadataUpdateResult and its WithRefusals variant. store is the
// refusal counter that records a refused required adoption and clears on
// success; nil falls back to the in-memory store.
func execVirtualFileMetadataUpdateResult(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs, store VirtualAdoptionRefusalStore) (VirtualFileMetadataUpdateResult, error) {
	if db == nil {
		return VirtualFileMetadataUpdateResult{}, nil
	}
	// A real database and the durable refusal store together commit the
	// evidence write and the exact-pin refusal reconciliation in one
	// transaction. The in-memory fallback (tests, non-database deployments) has
	// no cross-process window to close and stays on the best-effort path, as does
	// a database that cannot begin a transaction.
	if txDB, ok := db.(VirtualFileMetadataTxBeginner); ok {
		if txStore, ok := store.(VirtualAdoptionRefusalTxStore); ok {
			return execVirtualFileMetadataUpdateResultTx(ctx, txDB, txStore, args)
		}
	}
	return execVirtualFileMetadataUpdateResultUntx(ctx, db, args, store)
}

// virtualMetadataStatement holds the precomputed argument pieces the saver
// shares between its transactional and non-transactional paths, so both run the
// identical VirtualFileMetadataUpdateSQL with identical fences.
type virtualMetadataStatement struct {
	videoJSON                  string
	audioJSON                  string
	subtitleJSON               string
	providerRequestHeadersJSON any
	fenceVerdict               bool
}

func newVirtualMetadataStatement(args models.VirtualFilePersistArgs) (virtualMetadataStatement, error) {
	vStr := string(args.VideoTracks)
	if vStr == "" || vStr == jsonNullLiteral {
		vStr = "[]"
	}
	aStr := string(args.AudioTracks)
	if aStr == "" || aStr == jsonNullLiteral {
		aStr = "[]"
	}
	sStr := string(args.SubtitleTracks)
	if sStr == "" || sStr == jsonNullLiteral {
		sStr = "[]"
	}
	// A nil/empty header map is passed as nil so the SQL COALESCE preserves the
	// stored set; serializeJSONB would do the same, but marshal here so the
	// argument shape stays a single jsonb placeholder.
	var providerRequestHeadersJSON any
	if len(args.ProviderRequestHeaders) > 0 {
		encoded, marshalErr := json.Marshal(args.ProviderRequestHeaders)
		if marshalErr != nil {
			return virtualMetadataStatement{}, fmt.Errorf("marshal provider request headers: %w", marshalErr)
		}
		providerRequestHeadersJSON = encoded
	}
	return virtualMetadataStatement{
		videoJSON:                  vStr,
		audioJSON:                  aStr,
		subtitleJSON:               sStr,
		providerRequestHeadersJSON: providerRequestHeadersJSON,
		// The verdict fence is only meaningful where the caller needs a
		// confirmed adoption (RequireAdopt) and has not asked for an explicit
		// retry.
		fenceVerdict: args.RequireAdopt && !args.AllowFailedVerdict,
	}, nil
}

// run executes VirtualFileMetadataUpdateSQL for one attempt against db, which is
// a pool or an open transaction. adoptPath is the path this attempt nominates
// (empty on the metadata-only retry). A pgx.ErrNoRows is reported as an empty
// persisted path (a CAS/fence miss); any other error is returned unchanged.
func (s virtualMetadataStatement) run(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs, adoptPath string) (string, error) {
	neutralPath := ""
	if adoptPath != "" {
		neutralPath = virtualPlaybackNeutralKey(adoptPath)
	}
	// The atomic guard only applies when the caller needs a confirmed adoption;
	// the metadata-only retry clears adoptPath and therefore writes evidence on
	// the row's current path as before.
	requireAdoption := args.RequireAdopt && adoptPath != ""
	// A required adoption of a different release replaces the stored transport
	// fields as a set ($30): an omitted identity tier, URL or header set belongs
	// to the release the row is leaving and must not survive under the new path.
	// The release test is the durable identity, not the item-level neutral key:
	// the ordinary replacement virtual://movie/tt100?result=a -> ?result=b shares
	// a neutral key, so a key comparison would preserve A's fields under B. A
	// metadata-only retry (adoptPath cleared) and a proven same-release adoption
	// keep the preserve-on-omission behavior.
	replaceIdentity := requireAdoption && virtualPersistAdoptionReplacesTransport(args, adoptPath)
	var persistedPath string
	err := db.QueryRow(ctx, VirtualFileMetadataUpdateSQL,
		s.videoJSON, s.audioJSON, s.subtitleJSON, args.Resolution, args.CodecVideo, args.CodecAudio, args.Container, args.HDR, args.Bitrate, args.Duration,
		args.FileID, args.ExpectedFilePath, args.StampProbe,
		args.UpdatedAt, args.ProbeUpdatedAt, args.OwnerID, args.LibraryID, adoptPath,
		neutralPath, virtualFailedVerdictMaxAge.Seconds(), s.fenceVerdict, requireAdoption,
		args.ResolvedURL, args.ResolvedURLExpiresAt,
		args.ProviderVideoHash, args.ProviderGUID, args.ProviderReleaseName, args.ProviderReleaseSize,
		s.providerRequestHeadersJSON, replaceIdentity, args.ClearProbe,
		args.ReconcileCollectionVariant,
	).Scan(&persistedPath)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row matched the CAS fence: a stale snapshot, reported as a
		// zero-row miss by the caller.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return persistedPath, nil
}

// execVirtualFileMetadataUpdateResultUntx is the best-effort saver used when no
// transaction is available. The exact-pin clear is a separate post-write call;
// see execVirtualFileMetadataUpdateResultTx for the atomic production path.
func execVirtualFileMetadataUpdateResultUntx(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs, store VirtualAdoptionRefusalStore) (VirtualFileMetadataUpdateResult, error) {
	statement, err := newVirtualMetadataStatement(args)
	if err != nil {
		return VirtualFileMetadataUpdateResult{}, err
	}
	persistedPath, err := statement.run(ctx, db, args, args.AdoptPath)
	if err == nil && persistedPath == "" {
		// The best-effort saver has no row lock, so a stale CAS snapshot gets
		// one re-read and one retry against the current state. A provider
		// re-list can advance updated_at without changing the probe stamp; a
		// metadata-only write is re-based only while the exact path, owner and
		// probe stamp still match. A churned ?result= token is re-based only for
		// a required adoption. A second miss stays a refusal.
		persistedPath, args, err = retryVirtualMetadataUpdate(ctx, db, args, statement)
		if err != nil {
			return VirtualFileMetadataUpdateResult{}, err
		}
	}
	if err == nil {
		if persistedPath == "" {
			if args.RequireAdopt && args.AdoptPath != "" {
				// The atomic guard matched no row: the adoption was refused
				// (sibling owner, collection row, live failed verdict) or the
				// CAS snapshot was stale. Either way the validated identity
				// was not adopted, and the tracks/stamp were not written. The
				// caller classifies the concrete cause against the fence's own
				// precedence and carries it in RefusalReason; a caller that did
				// not classify logs a neutral reason rather than a lumped claim.
				reason := args.RefusalReason
				if reason == "" {
					reason = virtualProbeRefusalUnclassified
				}
				refusals := recordAdoptionRefusalStore(ctx, store, args.FileID, args.ExpectedFilePath)
				slog.WarnContext(ctx, "virtual probe evidence persist refused: required identity adoption matched no row",
					"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath,
					"expected_path", args.ExpectedFilePath,
					virtualEvidenceLogKeyReason, reason, "consecutive_refusals", refusals,
					"refusal_bound", virtualAdoptionRefusalBound,
					"bound_reached", refusals >= virtualAdoptionRefusalBound)
				return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s was not adopted", errVirtualAdoptIdentityNotPersisted, args.AdoptPath)
			}
			return VirtualFileMetadataUpdateResult{}, nil
		}
		if args.RequireAdopt && args.AdoptPath != "" && persistedPath != args.AdoptPath {
			// The metadata and stamp landed, but the validated identity did
			// not. Reporting success here would let the fallback serve a
			// substitute the catalog row does not own.
			return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s persisted as %q", errVirtualAdoptIdentityNotPersisted, args.AdoptPath, persistedPath)
		}
		// The write landed. Any consecutive-refusal count for this row and pin is
		// stale: the row now carries evidence, so the bound resets.
		clearAdoptionRefusalStore(ctx, store, args.FileID, args.ExpectedFilePath)
		return VirtualFileMetadataUpdateResult{
			RowsAffected:    1,
			MetadataUpdated: true,
			IdentityAdopted: args.AdoptPath != "" && persistedPath == args.AdoptPath,
		}, nil
	}
	var pgErr *pgconn.PgError
	if args.AdoptPath == "" || !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return VirtualFileMetadataUpdateResult{}, err
	}
	if args.RequireAdopt {
		// A collision is proof the validated identity was not adopted. A
		// metadata-only retry would stamp the substitute's tracks and probe
		// evidence on a row that does not own them, so refuse outright.
		slog.WarnContext(ctx, "virtual probe evidence persist adoption collided with an existing path owner; refusing without writing tracks",
			"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath, "error", err)
		return VirtualFileMetadataUpdateResult{}, fmt.Errorf("%w: candidate %s collided with an existing path owner", errVirtualAdoptIdentityNotPersisted, args.AdoptPath)
	}
	slog.WarnContext(ctx, "virtual probe evidence persist adoption raced an existing path owner; retrying without adoption",
		"component", "api", "file_id", args.FileID, "adopt_path", args.AdoptPath, "error", err)
	// The retry deliberately retains evidence on the current row, but it did
	// not adopt the requested identity. Callers must not treat this as an
	// adoption success (metadata-only retries are not identity proof).
	retryPath, retryErr := statement.run(ctx, db, args, "")
	if retryErr != nil {
		return VirtualFileMetadataUpdateResult{}, retryErr
	}
	if retryPath == "" {
		// The metadata-only retry also missed the CAS fence.
		return VirtualFileMetadataUpdateResult{}, nil
	}
	// The retry wrote fresh evidence on the current row (it deliberately did not
	// adopt the requested identity). The row now carries evidence, so any
	// consecutive-refusal count for this row and pin is stale and must be
	// cleared exactly as the ordinary success path does; otherwise a later
	// unrelated refusal would inherit the pre-success count and reach the
	// terminal threshold prematurely.
	clearAdoptionRefusalStore(ctx, store, args.FileID, args.ExpectedFilePath)
	return VirtualFileMetadataUpdateResult{
		RowsAffected:    1,
		MetadataUpdated: true,
		IdentityAdopted: false,
	}, nil
}

// ExecVirtualFileMetadataUpdate preserves the historical row-count contract.
func ExecVirtualFileMetadataUpdate(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs) (int64, error) {
	return execVirtualFileMetadataUpdateRows(ExecVirtualFileMetadataUpdateResult(ctx, db, args))
}

// ExecVirtualFileMetadataUpdateWithRefusals is ExecVirtualFileMetadataUpdate
// with an explicit durable refusal store.
func ExecVirtualFileMetadataUpdateWithRefusals(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs, store VirtualAdoptionRefusalStore) (int64, error) {
	return execVirtualFileMetadataUpdateRows(ExecVirtualFileMetadataUpdateResultWithRefusals(ctx, db, args, store))
}

func execVirtualFileMetadataUpdateRows(result VirtualFileMetadataUpdateResult, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	if !result.MetadataUpdated {
		return 0, nil
	}
	return result.RowsAffected, nil
}

// virtualSnapshot captures a catalog row's identity and generation before
// resolution/probing so the CAS fence can detect stale writes.
type virtualSnapshot struct {
	FileID         int
	FilePath       string
	UpdatedAt      time.Time
	ProbeUpdatedAt *time.Time
	OwnerID        int
	LibraryID      int
}

func snapshotVirtualRow(file *models.MediaFile) virtualSnapshot {
	return virtualSnapshot{
		FileID:         file.ID,
		FilePath:       file.FilePath,
		UpdatedAt:      file.UpdatedAt,
		ProbeUpdatedAt: file.ProbeUpdatedAt,
		OwnerID:        file.VirtualOwnerInstallationID,
		LibraryID:      file.MediaFolderID,
	}
}

// virtualRowCASState is the row state the adoption CAS fences on. It is read
// again only after a required adoption matched no row: a provider re-list or a
// rematch that committed between the caller's snapshot and the write changes
// file_path (a churned ?result= token), updated_at and/or probe_updated_at, so
// the CAS misses even though the row is still the same item. Retrying against
// the current state keeps the evidence instead of recording an adoption refusal
// and eventually condemning the candidate.
type virtualRowCASState struct {
	FilePath       string
	UpdatedAt      time.Time
	ProbeUpdatedAt *time.Time
	OwnerID        int
	LibraryID      int
	ProbeSource    string
	VideoHash      string
	GUID           string
	ReleaseName    string
	ReleaseSize    int64
}

const virtualRowCASStateSQL = `
SELECT file_path,
       updated_at,
       probe_updated_at,
       COALESCE(virtual_owner_installation_id, 0),
       COALESCE(media_folder_id, 0),
       COALESCE(probe_source, ''),
       COALESCE(provider_video_hash, ''),
       COALESCE(provider_guid, ''),
       COALESCE(provider_release_name, ''),
       COALESCE(provider_release_size, 0)
FROM media_files
WHERE id = $1`

// readVirtualRowCASState reads the current CAS state for one row. A missing row
// reports found=false without an error; any other failure is returned so the
// caller keeps the caller's snapshot rather than guessing.
func readVirtualRowCASState(ctx context.Context, db VirtualFileMetadataDB, fileID int) (virtualRowCASState, bool, error) {
	if db == nil || fileID <= 0 {
		return virtualRowCASState{}, false, nil
	}
	var state virtualRowCASState
	err := db.QueryRow(ctx, virtualRowCASStateSQL, fileID).Scan(
		&state.FilePath, &state.UpdatedAt, &state.ProbeUpdatedAt,
		&state.OwnerID, &state.LibraryID, &state.ProbeSource,
		&state.VideoHash, &state.GUID, &state.ReleaseName, &state.ReleaseSize,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return virtualRowCASState{}, false, nil
	}
	if err != nil {
		return virtualRowCASState{}, false, err
	}
	return state, true, nil
}

// refreshVirtualPersistArgsForRow re-bases a required-adoption write on the
// row's current state. The refresh is limited to a row that still belongs to
// the same item/profile (the same provider-neutral key): a provider re-list
// churns the ?result= token but never the neutral key, while a row that moved
// to a different item/profile keeps the caller's snapshot and is refused.
func refreshVirtualPersistArgsForRow(args models.VirtualFilePersistArgs, current virtualRowCASState) models.VirtualFilePersistArgs {
	if args.ExpectedFilePath == "" || current.FilePath == "" {
		return args
	}
	if virtualPlaybackNeutralKey(args.ExpectedFilePath) != virtualPlaybackNeutralKey(current.FilePath) {
		return args
	}
	args.ExpectedFilePath = current.FilePath
	args.UpdatedAt = current.UpdatedAt
	args.ProbeUpdatedAt = current.ProbeUpdatedAt
	args.OwnerID = current.OwnerID
	args.LibraryID = current.LibraryID
	args.ExpectedProviderVideoHash = current.VideoHash
	args.ExpectedProviderGUID = current.GUID
	args.ExpectedProviderReleaseName = current.ReleaseName
	args.ExpectedProviderReleaseSize = current.ReleaseSize
	return args
}

// virtualPersistSnapshotMoved decides whether a zero-row CAS write may be
// retried once against the row's current state.
//
// A required adoption follows a churned ?result= token: the provider-neutral
// key proves the new path is still the same item/profile, so the write is
// re-based onto it. That is the only case where a changed path is accepted.
//
// A metadata-only write is bound to the exact row bytes the probe enumerated.
// It may be re-based only while the path, owner and probe stamp are unchanged;
// the only difference allowed is a newer updated_at, which a provider re-list
// advances without changing any probe evidence. If another probe committed in
// the meantime, probe_updated_at differs and the write still refuses. A row
// that moved elsewhere keeps the caller's snapshot.
func virtualPersistSnapshotMoved(original models.VirtualFilePersistArgs, current virtualRowCASState, refreshed models.VirtualFilePersistArgs) bool {
	// The probe stamp is the authority on whether this evidence still describes
	// the row's current bytes. Check it before the adoption path can re-base:
	// otherwise a concurrent probe that moved both the concrete path and the
	// stamp could be overwritten by this older adoption.
	if !sameVirtualProbeStamp(original.ProbeUpdatedAt, current.ProbeUpdatedAt) {
		return false
	}
	if original.RequireAdopt && original.AdoptPath != "" &&
		original.ExpectedFilePath != refreshed.ExpectedFilePath {
		return true
	}
	if original.ExpectedFilePath == "" || original.ExpectedFilePath != current.FilePath {
		return false
	}
	if original.OwnerID != 0 && original.OwnerID != current.OwnerID {
		return false
	}
	if original.LibraryID != 0 && original.LibraryID != current.LibraryID {
		return false
	}
	return current.UpdatedAt.After(original.UpdatedAt)
}

func sameVirtualProbeStamp(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// retryVirtualMetadataUpdate performs the single bounded CAS retry after a
// zero-row metadata write. It returns the write's path, the arguments that
// produced it (so a caller can keep the re-based snapshot), and any terminal
// error. A read failure or a row that moved beyond the allowed churn is a
// plain miss: the caller keeps its original snapshot and reports no metadata
// update.
func retryVirtualMetadataUpdate(ctx context.Context, db VirtualFileMetadataDB, args models.VirtualFilePersistArgs, statement virtualMetadataStatement) (string, models.VirtualFilePersistArgs, error) {
	current, found, readErr := readVirtualRowCASState(ctx, db, args.FileID)
	if readErr != nil {
		return "", args, readErr
	}
	if !found {
		return "", args, nil
	}
	retried := refreshVirtualPersistArgsForRow(args, current)
	if !virtualPersistSnapshotMoved(args, current, retried) {
		return "", args, nil
	}
	path, err := statement.run(ctx, db, retried, retried.AdoptPath)
	if err != nil || path == "" {
		return "", args, err
	}
	return path, retried, nil
}

// persistVirtualMetadataBounded admits a probe-evidence write into the
// bounded, coalescing evidence buffer and reports the explicit admission
// result. It no longer writes synchronously: saturation is expressed as
// virtualEvidenceRejected rather than a silent drop, accepted work is retried
// with backoff by the buffer workers, and coalescing keeps the newest snapshot
// for a given row identity. Callers that need a synchronous, CAS-fenced write
// use ExecVirtualFileMetadataUpdate directly.
func (h *PlaybackHandler) persistVirtualMetadataBounded(ctx context.Context, snap virtualSnapshot, expectedFilePath string, file *models.MediaFile, stampProbe bool) (virtualEvidenceAdmission, error) {
	if h == nil || h.VirtualFileSaver == nil || file == nil || snap.FileID <= 0 {
		return virtualEvidenceRejected, nil
	}
	videoJSON := marshalTracksJSON(sanitizeTrackSlice(file.VideoTracks))
	audioJSON := marshalTracksJSON(sanitizeTrackSlice(file.AudioTracks))
	subJSON := marshalTracksJSON(sanitizeTrackSlice(file.SubtitleTracks))
	res, vCodec, aCodec, container, hdr, bitrate, duration := file.Resolution, file.CodecVideo, file.CodecAudio, file.Container, file.HDR, file.Bitrate, file.Duration
	return h.enqueueVirtualProbeEvidence(ctx, models.VirtualFilePersistArgs{
		FileID:           snap.FileID,
		ExpectedFilePath: expectedFilePath,
		VideoTracks:      videoJSON,
		AudioTracks:      audioJSON,
		SubtitleTracks:   subJSON,
		Resolution:       res,
		CodecVideo:       vCodec,
		CodecAudio:       aCodec,
		Container:        container,
		HDR:              hdr,
		Bitrate:          bitrate,
		Duration:         duration,
		StampProbe:       stampProbe,
		UpdatedAt:        snap.UpdatedAt,
		ProbeUpdatedAt:   snap.ProbeUpdatedAt,
		OwnerID:          snap.OwnerID,
		LibraryID:        snap.LibraryID,
	}), nil
}

// persistVirtualProbeEvidence writes probed track inventory and the probe stamp
// back to the catalog row for the virtual content the client requested, so the
// next start of that row can reuse the persisted evidence instead of
// re-listing and re-probing.
//
// The probed candidate carries a resolved ?result= URI while the catalog row
// usually stores the neutral URI, so the metadata update's file_path guard
// would miss the row. Non-collection rows adopt the resolved URI onto the row
// first (mirroring the stale-fallback path) so the guard matches and the row
// owns the pinned candidate on the next start. Collection-owned rows are never
// rewritten: the collection sync reconciles their file_path against its
// desired set and would delete an adopted path as stale. They are stamped in
// place under their neutral path instead, which is still enough for the
// repeat-play gates (cache/pin + probe stamp + complete evidence) to fire.
//
// The write is queued to the bounded evidence worker pool rather than spawned
// per call. That keeps it off the aggregate detached-work gate (a burst of long
// probes must not crowd evidence out) while still bounding memory; see
// enqueueVirtualProbeEvidence for the overload behavior.
//
// foreground marks the caller as the foreground request's own candidate. When
// the bounded pool rejects that write, the candidate gets one bounded direct
// write instead (see persistVirtualEvidenceDirect) so a saturated pool cannot
// leave the row permanently unprobed and force the slow list+probe path on
// every later play. Background and speculative callers pass false and are never
// written directly, so the fallback cannot amplify load.
func (h *PlaybackHandler) persistVirtualProbeEvidence(ctx context.Context, catalogFile *models.MediaFile, resolvedPath string, probed *models.MediaFile, candidate resolver.PersistedIdentityTiers, stampProbe, foreground bool) {
	args, originFileID, ok := h.virtualProbeEvidenceArgs(ctx, catalogFile, resolvedPath, probed, candidate, stampProbe)
	if !ok {
		return
	}
	// The evidence buffer owns the write context and retry policy; ctx is used
	// only to tie a rejection to the caller's request. Admission is explicit so
	// the caller can distinguish an accepted, a coalesced (superseded but
	// still represented) and a rejected (buffer full) write.
	switch h.enqueueVirtualProbeEvidenceFor(ctx, args, originFileID) {
	case virtualEvidenceRejected:
		slog.ErrorContext(ctx, "virtual probe evidence persist rejected: evidence buffer full",
			"component", "api", "file_id", args.FileID, "stamp_probe", args.StampProbe, "foreground", foreground)
		if foreground && h.persistVirtualEvidenceDirect(ctx, args) {
			slog.InfoContext(ctx, "virtual probe evidence persisted via direct fallback after buffer rejection",
				"component", "api", "file_id", args.FileID, "stamp_probe", args.StampProbe)
		}
	}
}

// virtualProbeEvidenceArgs builds the catalog write for one probe result. It
// returns false when the write must be refused: nil inputs, a row without an id
// or saver, a cross-release candidate the row cannot adopt, or a candidate whose
// path ownership the identity guard could not answer. The second return value is
// the row the probe was requested for, so a caller can distinguish a write to
// the requested row from a rotation onto a sibling owner.
//
// When verified evidence arrives for a URI a sibling row verifiably owns, the
// write rotates to that owner row instead of attempting a CAS adoption the SQL
// sibling fence refuses (see virtualProbeEvidenceRotateTarget). That is the
// background-probe fix: a probed candidate URI whose path already belongs to an
// alternate-version row must bind the evidence to the version that actually
// plays, not be dropped onto the pinned row forever.
func (h *PlaybackHandler) virtualProbeEvidenceArgs(ctx context.Context, catalogFile *models.MediaFile, resolvedPath string, probed *models.MediaFile, candidate resolver.PersistedIdentityTiers, stampProbe bool) (models.VirtualFilePersistArgs, int, bool) {
	if h == nil || (h.VirtualFileSaver == nil && h.VirtualFileMetadataSaver == nil) || catalogFile == nil || probed == nil || catalogFile.ID <= 0 {
		return models.VirtualFilePersistArgs{}, 0, false
	}
	identityRow, evidence, ok := h.virtualProbeEvidenceRotateTarget(ctx, catalogFile, resolvedPath, probed, candidate)
	if !ok {
		return models.VirtualFilePersistArgs{}, 0, false
	}
	args, ok := h.virtualProbeEvidenceArgsForRow(ctx, identityRow, resolvedPath, evidence, candidate, stampProbe)
	if !ok {
		return models.VirtualFilePersistArgs{}, 0, false
	}
	return args, catalogFile.ID, true
}

// virtualProbeEvidenceRotateTarget resolves the row the evidence belongs to and
// the evidence to write. It returns the requested row and the probed file
// unchanged for every same-release or ordinary cross-release case. When the
// candidate's concrete path is verifiably owned by a sibling row of this
// content, it returns that owner row's catalog identity overlaid with the
// freshly probed tracks (the rotateVirtualSourceToOwnerRow pattern), so the
// write lands on the row that owns the bytes and the existing inventory_updated
// publish carries the verified inventory to every menu bound to it.
//
// The owner question goes through virtualPathOwnerRow and nothing else: the
// guard asserts ContentID, EpisodeID, owner installation, library and the exact
// concrete path, and fails closed on a lookup error or an incomplete row. A
// lookup error is an infrastructure failure, not an identity verdict: it backs
// the ownership question off for a short window and never advances the
// adoption-refusal bound, so a database outage cannot condemn healthy content.
// A refusal to name an owner for a path a sibling actually holds keeps the
// previous behavior: the write is refused (or classified and left to the SQL
// fence) rather than adopting bytes whose owner is unknown. Every refusal is
// logged with the concrete cause, never a lumped bucket.
func (h *PlaybackHandler) virtualProbeEvidenceRotateTarget(ctx context.Context, catalogFile *models.MediaFile, resolvedPath string, probed *models.MediaFile, candidate resolver.PersistedIdentityTiers) (*models.MediaFile, *models.MediaFile, bool) {
	if virtualProbeEvidenceSharesRowRelease(catalogFile, resolvedPath, candidate) {
		return catalogFile, probed, true
	}
	backoffKey := virtualOwnerLookupBackoffKey(catalogFile.ID, resolvedPath)
	if virtualOwnerLookups.recent(backoffKey) {
		// A previous ownership lookup failed and its short backoff is still
		// active: skip re-asking a failing store on every probe. The write stays
		// fenced and the live session is served in memory. The window lapses on
		// its own and a later successful lookup clears it, so nothing here is
		// durable and nothing condemns the row.
		slog.DebugContext(ctx, "virtual probe evidence: path ownership lookup is backing off after a store failure",
			"component", "api", "file_id", catalogFile.ID, "candidate_uri", resolvedPath)
		h.publishRefusedProbeInventory(ctx, catalogFile.ID, resolvedPath, probed)
		return nil, nil, false
	}
	ownerRow, ownerErr := h.virtualPathOwnerRow(ctx, catalogFile, resolvedPath, catalogFile.VirtualOwnerInstallationID, catalogFile.MediaFolderID)
	if ownerErr != nil {
		// An unanswered ownership question is not safe absence: fail closed
		// rather than adopt or stamp bytes whose owner is unknown. But the store
		// could not answer, which is an infrastructure failure, not an identity
		// verdict: it must never advance the adoption-refusal bound or install
		// the durable terminal failed_at verdict, or a database outage would
		// suppress healthy content. Back the ownership question off for a short
		// window and serve the live session in memory; the next probe after the
		// window re-asks.
		virtualOwnerLookups.mark(backoffKey)
		slog.WarnContext(ctx, "virtual probe evidence refused: candidate path ownership lookup failed; backing off without counting",
			"component", "api", "file_id", catalogFile.ID, "candidate_uri", resolvedPath,
			"row_path", catalogFile.FilePath, "probe_source", catalogFile.ProbeSource,
			virtualEvidenceLogKeyReason, virtualProbeRefusalOwnerLookupFailed,
			virtualEvidenceLogKeyError, ownerErr,
			"backoff_seconds", virtualOwnerLookups.window().Seconds(),
			"terminal", false)
		// The catalog write is correctly blocked, but the live session must not
		// stay stale: serve the probed tracks in memory to sessions bound to
		// this row's exact candidate. The publish is best-effort and writes
		// nothing to the catalog.
		h.publishRefusedProbeInventory(ctx, catalogFile.ID, resolvedPath, probed)
		return nil, nil, false
	}
	// The lookup answered: any prior backoff for this row and candidate path is
	// stale.
	virtualOwnerLookups.clear(backoffKey)
	if ownerRow != nil && ownerRow.ID != catalogFile.ID {
		sameRelease := virtualSiblingOwnerSharesRelease(catalogFile, ownerRow)
		// A rotation may adopt the owner's identity, never a poorer track
		// menu than the selected row offered. The probe measured the bytes
		// this write describes, so compare that inventory.
		if virtualRotationWouldDropTracks(catalogFile, ownerRow, probed) {
			if sameRelease {
				// The bytes are the selected row's own release and the
				// viewer picked that row. Keep the measured inventory on it
				// instead of moving it to a duplicate with a poorer menu.
				return catalogFile, probed, true
			}
			slog.WarnContext(ctx, "virtual probe evidence rotation refused: owner row would lose audio or subtitle tracks",
				"component", "api", "requested_file_id", catalogFile.ID, "owner_file_id", ownerRow.ID,
				"candidate_uri", resolvedPath,
				"selected_audio_tracks", len(catalogFile.AudioTracks), "selected_subtitle_tracks", len(catalogFile.SubtitleTracks),
				"probed_audio_tracks", len(probed.AudioTracks), "probed_subtitle_tracks", len(probed.SubtitleTracks),
				virtualEvidenceLogKeyReason, "sibling_owner_track_loss")
			// Keep the live session's menu useful without moving the bytes:
			// publish the measured inventory to sessions already bound to this
			// candidate, and write nothing to a row that would lose tracks.
			h.publishRefusedProbeInventory(ctx, catalogFile.ID, resolvedPath, probed)
			return nil, nil, false
		}
		if sameRelease {
			// Duplicate catalog rows for one release share candidate URIs and
			// neutral keys. Rotating here would overlay the requested row's
			// probed inventory onto its duplicate sibling (and, on the reverse
			// probe, the sibling's onto the requested row): exactly the silent
			// cross-row contamination this guard exists to stop. The refusal is
			// safe, but it is also unbounded: a duplicate row whose pin never
			// adopts keeps serving provisional evidence forever. Count it under
			// the media_files row lock (with the durable store), and after the
			// bound rotate the evidence onto the owning row anyway — both rows
			// provably carry the same release, so the probed bytes are that
			// release's bytes and no foreign inventory crosses rows. Taking the
			// row lock first keeps this increment serialized with the evidence
			// writes and clears on the same row instead of racing them.
			refusals := h.adoptionRefusalStoreCount(ctx, catalogFile.ID, catalogFile.FilePath)
			if refusals >= virtualAdoptionRefusalBound {
				rotated := rotateVirtualSourceToOwnerRow(probed, ownerRow, resolvedPath)
				slog.InfoContext(ctx, "virtual probe evidence rotation to same-release owner row after bounded refusals",
					"component", "api", "requested_file_id", catalogFile.ID, "owner_file_id", ownerRow.ID,
					"candidate_uri", resolvedPath, "consecutive_refusals", refusals,
					virtualEvidenceLogKeyReason, "sibling_owner_same_release_bound_rotation")
				return rotated, rotated, true
			}
			slog.WarnContext(ctx, "virtual probe evidence rotation refused: sibling owner row is a duplicate of the same release",
				"component", "api", "requested_file_id", catalogFile.ID, "owner_file_id", ownerRow.ID,
				"candidate_uri", resolvedPath, virtualEvidenceLogKeyReason, "sibling_owner_same_release",
				"consecutive_refusals", refusals, "refusal_bound", virtualAdoptionRefusalBound)
			h.publishRefusedProbeInventory(ctx, catalogFile.ID, resolvedPath, probed)
			return nil, nil, false
		}
		// The candidate's path is an existing alternate version of a genuinely
		// different release. Rotate the binding to its owner row: keep the
		// owner's catalog identity and CAS snapshot (the current generation) and
		// overlay the freshly probed tracks so the verified inventory is not
		// lost. The write is then a same-release metadata update on a row that
		// already owns the path, so the SQL adoption fence is not involved.
		rotated := rotateVirtualSourceToOwnerRow(probed, ownerRow, resolvedPath)
		slog.InfoContext(ctx, "virtual probe evidence rotated to the candidate's owner row",
			"component", "api", "requested_file_id", catalogFile.ID, "owner_file_id", ownerRow.ID,
			"candidate_uri", resolvedPath, "reason", "sibling_owner_rotation")
		return rotated, rotated, true
	}
	return catalogFile, probed, true
}

// virtualSiblingOwnerSharesRelease reports whether a sibling owner row
// verifiably carries the same release as the requested row, judged by the
// durable provider identity compared tier-by-tier (video hash, then source
// GUID, then normalized release name plus a plausibly-equal size). Two rows are
// the same release only when a non-empty tier they both carry agrees; an
// identity-less row is not proof.
//
// It exists to keep the owner-row rotation from silently repainting a duplicate
// row of the same release with the requested row's inventory. Distinct rows of
// one release share candidate URIs, so the candidate URI alone cannot tell them
// apart; only the durable identity can. A genuinely different release (distinct
// hash/GUID/name+size) does not compare equal and still rotates, which is the
// alternate-version behavior the fallback relies on.
//
// The comparison is symmetric across compatible tiers rather than a single
// strongest-tier key: a name-only row keys as name+size while a GUID/hash row
// keys in that stronger tier, so a strongest-tier equality would read two rows
// of one release as distinct and rotate onto the duplicate. Failing to prove
// the tiers compatible is a refusal (the caller treats false as "not the same
// release"), so an ambiguous pair fails closed instead of repainting a sibling.
func virtualSiblingOwnerSharesRelease(a, b *models.MediaFile) bool {
	if a == nil || b == nil {
		return false
	}
	identityA := resolver.NewPersistedIdentityTiers(a.ProviderVideoHash, a.ProviderGUID, a.ProviderReleaseName, a.ProviderReleaseSize)
	identityB := resolver.NewPersistedIdentityTiers(b.ProviderVideoHash, b.ProviderGUID, b.ProviderReleaseName, b.ProviderReleaseSize)
	_, shared := resolver.PersistedIdentitiesMatch(identityA, identityB)
	return shared
}

// virtualProbeEvidenceSharesRowRelease reports whether probe evidence for a
// candidate belongs to the row's own release. The exact and provider-neutral
// ownership checks (virtualCandidateRowVerified) decide the common case; a
// provider that renumbered its per-listing result ids surfaces the same release
// under a new concrete id, which a comparison of the row's durable identity
// against the identity the resolver reported proves is the same release. Such a
// churned candidate is the row's own evidence and is written against the row's
// pinned path rather than requiring a fenced cross-release adoption of the new
// token. A genuinely different release shares no tier, so it keeps the existing
// cross-release path.
//
// The candidate identity is the resolver's own result, never the probed
// transient: that file is seeded from the requested row and would echo the row's
// tiers back as false proof (see withResolvedCandidate). The comparison is the
// asymmetric SharedTier with the row as the authoritative persisted selection, so
// a stronger tier the row carries but the candidate cannot corroborate is a veto
// (a missing hash is not neutral), and the name tier cannot match on size alone.
func virtualProbeEvidenceSharesRowRelease(row *models.MediaFile, candidateURI string, candidate resolver.PersistedIdentityTiers) bool {
	if row == nil {
		return false
	}
	if virtualCandidateRowVerified(row, candidateURI) {
		return true
	}
	rowTiers := resolver.NewPersistedIdentityTiers(row.ProviderVideoHash, row.ProviderGUID, row.ProviderReleaseName, row.ProviderReleaseSize)
	_, shared := rowTiers.SharedTier(candidate)
	return shared
}

// virtualProbeEvidenceArgsForRow builds the catalog write for one probe result
// against the row the evidence belongs to. It returns false when the write must
// be refused: a cross-release candidate the row cannot adopt (a collection-owned
// row with no adoption target).
func (h *PlaybackHandler) virtualProbeEvidenceArgsForRow(ctx context.Context, catalogFile *models.MediaFile, resolvedPath string, probed *models.MediaFile, candidate resolver.PersistedIdentityTiers, stampProbe bool) (models.VirtualFilePersistArgs, bool) {
	snap := snapshotVirtualRow(catalogFile)
	expectedPath := catalogFile.FilePath
	adoptPath := ""
	if resolvedPath != "" && resolvedPath != catalogFile.FilePath && catalogFile.ProbeSource != virtualCollectionProbeSource {
		adoptPath = resolvedPath
	}
	// Evidence for a different concrete release than this row verifiably owns
	// must never be folded in as metadata-only enrichment: the original row
	// would keep its own identity while acquiring the substitute's tracks and
	// probe stamp, which is exactly how a sibling-owner collision used to leak
	// the candidate's inventory onto the wrong row. Such a write is admitted
	// only with RequireAdopt set, so the SQL fence accepts or refuses the whole
	// write atomically. The same-release cases stay metadata-only: the row
	// carries the candidate's exact identity, or the provider-neutral identity
	// with no concrete pick (a neutral row owns every candidate in its release),
	// or the row's durable identity proves a provider-renumbered candidate is the
	// same release under a new listing token.
	crossRelease := !virtualProbeEvidenceSharesRowRelease(catalogFile, resolvedPath, candidate)

	// The write seam must not re-decide the size question. The caller already
	// classified the probe once (ClassifyVirtualServedEvidenceForProbe), and a
	// second, differently-keyed classification here is what made a path accept
	// evidence another path refused. Only the positive identity contradictions
	// are enforced again, so a wrong file cannot be stamped even by a caller
	// that skipped classification, while a served-size gap never blocks an
	// inventory write.
	if stampProbe && probed != nil {
		expectedRuntime := catalogFile.VirtualExpectedRuntimeMinutes
		if expectedRuntime <= 0 {
			expectedRuntime = probed.VirtualExpectedRuntimeMinutes
		}
		if expectedRuntime <= 0 {
			expectedRuntime = h.virtualExpectedRuntimeMinutes(ctx, catalogFile)
		}
		// A track-layout contradiction is decided at the acceptance seam,
		// where the probe input carries the declaration. This backstop only
		// re-enforces the duration contradiction, so an incomplete caller
		// result cannot be mistaken for an audio-only file here.
		if scanner.VirtualDurationContradictsDeclaration(probed.Duration, expectedRuntime) {
			slog.WarnContext(ctx, "virtual probe evidence refused: probe contradicts the declared release",
				"component", "api", "file_id", catalogFile.ID,
				"declared_size", catalogFile.FileSize, "probed_size", probed.ProbedFormatSize,
				"probed_duration_seconds", probed.Duration, "expected_runtime_minutes", expectedRuntime)
			return models.VirtualFilePersistArgs{}, false
		}
	}
	// Adopt against the session pin: when the row already carries a concrete
	// ?result= pick and only durable identity ties the resolved churned token to
	// it, keep the row's concrete path and write the evidence metadata-only
	// rather than moving the row onto the new token. A neutral row has no pin to
	// carry and still adopts the resolved concrete candidate, matching the
	// pre-existing pick.
	if !crossRelease && adoptPath != "" && !virtualCandidateRowVerified(catalogFile, resolvedPath) {
		adoptPath = ""
	}
	if crossRelease && adoptPath == "" {
		// A collection-owned row is never rewritten (the collection sync would
		// reconcile the adopted path away), so there is no adoption target to
		// fence the write on. Refuse instead of stamping another release's
		// inventory in place under the neutral path.
		slog.WarnContext(ctx, "virtual probe evidence refused: candidate belongs to a different release and the row cannot adopt it",
			"component", "api", "file_id", catalogFile.ID, "candidate_uri", resolvedPath,
			"row_path", catalogFile.FilePath, "probe_source", catalogFile.ProbeSource,
			"reason", virtualProbeRefusalCrossReleaseNoTarget)
		// The catalog write is correctly blocked, but the playing session's
		// menus must not stay empty: this probed candidate is the row's current
		// listing, so surface its tracks to live sessions still pinned to the
		// vanished candidate. Display only — the transport is never re-keyed,
		// and no replan is issued.
		h.publishOfferedReleaseInventory(ctx, catalogFile, resolvedPath, probed)
		return models.VirtualFilePersistArgs{}, false
	}
	args := models.VirtualFilePersistArgs{
		FileID:           snap.FileID,
		ExpectedFilePath: expectedPath,
		VideoTracks:      marshalTracksJSON(sanitizeTrackSlice(probed.VideoTracks)),
		AudioTracks:      marshalTracksJSON(sanitizeTrackSlice(probed.AudioTracks)),
		SubtitleTracks:   marshalTracksJSON(sanitizeTrackSlice(probed.SubtitleTracks)),
		Resolution:       probed.Resolution,
		CodecVideo:       probed.CodecVideo,
		CodecAudio:       probed.CodecAudio,
		Container:        probed.Container,
		HDR:              probed.HDR,
		Bitrate:          probed.Bitrate,
		Duration:         probed.Duration,
		// StampProbe is controlled by explicit successful-probe provenance: the
		// caller passes true only when the evidence it writes came from a probe
		// that succeeded (a fresh probe, a cache hit, or the durable deferred
		// write). It is never inferred from inventory shape, so a metadata-only
		// copy of existing tracks preserves the row's stamp instead of
		// advancing it from tracks it did not measure.
		StampProbe:     stampProbe,
		UpdatedAt:      snap.UpdatedAt,
		ProbeUpdatedAt: snap.ProbeUpdatedAt,
		// The row's own identity, for the release-replacement gate when this
		// write adopts a cross-release candidate path. The probe path carries no
		// candidate identity, so an adopted cross-release write replaces the
		// transport set (clearing the previous release's) rather than leaving
		// its URL, expiry, identity or headers attached to the new path.
		ExpectedProviderVideoHash:   catalogFile.ProviderVideoHash,
		ExpectedProviderGUID:        catalogFile.ProviderGUID,
		ExpectedProviderReleaseName: catalogFile.ProviderReleaseName,
		ExpectedProviderReleaseSize: catalogFile.ProviderReleaseSize,
		OwnerID:                     snap.OwnerID,
		LibraryID:                   snap.LibraryID,
		AdoptPath:                   adoptPath,
		// Cross-release evidence requires a confirmed identity adoption; the
		// atomic fence then refuses the entire write (tracks and stamp included)
		// when a sibling owns the target path or the candidate's verdict is
		// live-failed. A same-release write keeps the metadata-only contract.
		RequireAdopt: crossRelease,
	}
	if crossRelease {
		args.RefusalReason = h.virtualProbeEvidenceRefusalReason(ctx, catalogFile, resolvedPath)
		// The terminal decision is deliberately NOT made here. The refusal
		// reconciliation owns it: the saver records the increment and installs
		// the terminal from the live count, under the row lock and fenced on the
		// observed evidence revision (see txVirtualAdoptionRefusal). A count read
		// out here would race the evidence writes and clears that invalidate it.
	}
	return args, true
}

// virtualProbeEvidenceRefusalReason classifies a required adoption that is
// likely to be refused, mirroring the SQL fence's own predicate precedence so
// the persist log names the concrete cause instead of a lumped bucket. It is
// best-effort diagnosis: the saver only logs it when the write actually refuses,
// and a wrong guess would only mislabel a log line, never a write.
//
//   - a sibling row of this owner and library already owns the candidate path
//     (the identity guard declined to rotate to it) is sibling_owner;
//   - otherwise a live failed_at verdict on the candidate identity is
//     failed_verdict;
//   - otherwise the only remaining fence cause is a stale CAS snapshot.
func (h *PlaybackHandler) virtualProbeEvidenceRefusalReason(ctx context.Context, catalogFile *models.MediaFile, resolvedPath string) string {
	if catalogFile.ProbeSource == virtualCollectionProbeSource {
		return virtualProbeRefusalCollectionRow
	}
	rawOwner, capable, lookupErr := h.virtualPathLookupRow(ctx, resolvedPath)
	if lookupErr == nil && capable && rawOwner != nil && rawOwner.ID > 0 && rawOwner.ID != catalogFile.ID &&
		rawOwner.VirtualOwnerInstallationID == catalogFile.VirtualOwnerInstallationID &&
		rawOwner.MediaFolderID == catalogFile.MediaFolderID {
		return virtualProbeRefusalSiblingOwner
	}
	if verdictErr := h.virtualCandidateVerdictError(ctx, resolvedPath, catalogFile, catalogFile.VirtualOwnerInstallationID, false); verdictErr != nil {
		if errors.Is(verdictErr, ErrVirtualCandidateMarkedFailed) {
			return virtualProbeRefusalFailedVerdict
		}
	}
	return virtualProbeRefusalStaleSnapshot
}

// probeVirtualCandidateForegroundFallback probes the foreground request's own
// candidate synchronously under a short budget and persists the evidence
// directly when the detached probe gate is exhausted. Without it a gate-pressure
// burst can leave the row unprobed for this play, and every later play pays the
// slow list+probe path. One bounded attempt per foreground request keeps it from
// becoming a second worker pool; background/speculative candidates never reach
// this fallback because they simply skip the probe when the gate is full.
func (h *PlaybackHandler) probeVirtualCandidateForegroundFallback(
	requestCtx context.Context,
	stickyKey string,
	catalogFile *models.MediaFile,
	probeURL string,
	probeTransient models.MediaFile,
	probeCand VirtualPlaybackStream,
	candidate resolver.PersistedIdentityTiers,
	expectedRuntimeMinutes int,
	ownerInstallationID int,
) {
	if h == nil || catalogFile == nil {
		return
	}
	probeKey := virtualProbeFailureKey(probeCand.URI, ownerInstallationID)
	// Keep the values but drop request cancellation: the evidence write should
	// still land if the client has already disconnected.
	probeCtx, probeCancel := context.WithTimeout(context.WithoutCancel(requestCtx), virtualEvidenceFallbackBudget)
	defer probeCancel()
	forwardVirtualCandidateDeclaredAudio(&probeTransient, probeCand)
	probeTransient.VirtualExpectedRuntimeMinutes = expectedRuntimeMinutes
	probed, probeErr := h.probeVirtualSource(probeCtx, probeURL, &probeTransient, probeCand.RequestHeaders)
	if probeErr != nil || probed == nil {
		// A short-budget fallback timeout leaves the verdict unknown: do not
		// mark the failure damper or unpin a candidate the normal probe may yet
		// resolve. Only a definitive candidate-specific rejection counts.
		if !virtualProbeVerdictUnknown(probeErr) {
			virtualProbeFailures.mark(probeKey)
		}
		slog.WarnContext(requestCtx, "virtual foreground probe fallback failed",
			"component", "api", "candidate_uri", probeCand.URI, "error", probeErr)
		return
	}
	evidencePin := scanner.VirtualServedSizePin(probeCand.URI, ownerInstallationID)
	evidenceVerdict := classifyVirtualServedEvidenceV3(evidencePin, probeTransient.FileSize, &probeTransient, probed, expectedRuntimeMinutes)
	sizeGap := scanner.VirtualServedSizeMismatch(probeTransient.FileSize, probed.ProbedFormatSize)
	if !virtualRuntimePlausible(probed.Duration, expectedRuntimeMinutes) && !sizeGap {
		virtualProbeFailures.mark(probeKey)
		slog.WarnContext(requestCtx, "virtual foreground probe fallback rejected: probed duration implausible",
			"component", "api", "candidate_uri", probeCand.URI, "file_id", catalogFile.ID)
		h.unpinVirtualSticky(stickyKey, probeCand.URI)
		return
	}
	if evidenceVerdict == scanner.VirtualServedEvidenceMismatch {
		virtualProbeFailures.markSizeMismatch(probeKey, probed.ProbedFormatSize, probeTransient.FileSize)
		logVirtualProbeSizeMismatch(requestCtx, catalogFile.ID, probeCand.URI, probeTransient.FileSize, probed.ProbedFormatSize)
		h.unpinVirtualSticky(stickyKey, probeCand.URI)
		return
	}
	if sizeGap {
		logVirtualProbeSizeGapV3(requestCtx, catalogFile.ID, probeCand.URI, probeTransient.FileSize, probed.ProbedFormatSize, probed.Duration, expectedRuntimeMinutes,
			virtualProbeEvidenceWithholdsIdentity(evidenceVerdict))
	}
	virtualProbeFailures.clear(probeKey)
	virtualProbeFailures.clearSizeMismatch(probeKey)
	withholdsIdentity := virtualProbeEvidenceWithholdsIdentity(evidenceVerdict)
	if probeTransient.ID > 0 {
		probed.ID = probeTransient.ID
		probed.MediaFolderID = probeTransient.MediaFolderID
	}
	if probeTransient.Duration > 0 && probed.Duration <= 0 {
		probed.Duration = probeTransient.Duration
	}
	mergeVirtualCandidateTracks(probed, probeCand)
	// An unverified verdict writes metadata-only: the inventory is real, the
	// identity is not. See virtualProbeEvidenceWithholdsIdentity.
	args, _, ok := h.virtualProbeEvidenceArgs(requestCtx, catalogFile, probeCand.URI, probed, candidate, !withholdsIdentity)
	if !ok {
		return
	}
	if h.persistVirtualEvidenceDirect(requestCtx, args) {
		slog.InfoContext(requestCtx, "virtual probe evidence persisted via foreground fallback after detached gate exhaustion",
			"component", "api", "file_id", args.FileID, "candidate_uri", probeCand.URI)
	}
}

// errVirtualCandidateVerdictIncomplete reports that a verdict lookup found a
// row without a usable identity. It is deliberately distinct from a genuine
// not-found: a not-found means no catalog row owns the candidate (so there is
// no verdict to enforce), while an incomplete row means the verdict is unknown
// and must not be treated as eligible.
var errVirtualCandidateVerdictIncomplete = errors.New("virtual candidate verdict lookup returned an incomplete row")

// errVirtualCandidateVerdictUnknown marks a verdict decision that could not be
// made because the catalog lookup failed or returned an incomplete row. It is
// the fail-closed signal every candidate-selection loop watches for: a caller
// must not resolve, substitute or adopt a candidate while its verdict is
// unknowable, and must stop trying siblings rather than reinterpret the failure
// as "this candidate is dead".
var errVirtualCandidateVerdictUnknown = errors.New("virtual candidate verdict is unknown")

// errVirtualAdoptIdentityNotPersisted reports that the probe metadata was
// persisted but the validated candidate identity was not adopted onto the row.
// A uniqueness conflict, a probe_source guard, or the verdict fence can all
// leave file_path unchanged while the metadata UPDATE still matches one row.
// Callers that must report a confirmed identity adoption treat this as a
// failure; metadata-only evidence writers do not.
var errVirtualAdoptIdentityNotPersisted = errors.New("virtual candidate identity was not adopted")

// virtualAdoptionBarrier is a test seam invoked after the fallback's final
// verdict read and before the adoption write. Tests use it to commit a
// failed_at verdict in that window and prove the persistence-backed fence
// rejects the adoption rather than trusting the earlier read.
var virtualAdoptionBarrier func()

// virtualReleaseComparisonKey returns the release-identity key for a virtual
// URI: scheme/host/path plus every query parameter except the concrete
// "result=" pick and the quality "profile=" selection. A profile variant and
// the provider-neutral candidate for the same release therefore compare equal,
// while distinct result ids and distinct titles stay distinct.
//
// It is used only for same-release/adoption/evidence comparisons. The row's
// stored file_path keeps its profile so the version list still distinguishes
// quality variants; only the comparison drops it. The "result=" pick is left to
// the caller: sameVirtualReleaseIdentity compares it explicitly, and the
// neutral-ownership check in virtualCandidateRowVerified requires it absent.
func virtualReleaseComparisonKey(virtualPath string) string {
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return virtualPath
	}
	q := parsed.Query()
	q.Del("result")
	q.Del("profile")
	parsed.RawQuery = q.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

// sameVirtualReleaseIdentity reports whether two virtual URIs name the same
// release: byte-identical, or the same provider-neutral path (profile
// reconciled) carrying the same concrete ?result= candidate id. The profile is
// a selection qualifier the provider does not know, so the same provider result
// id under a profile variant and under the neutral key is one release.
func sameVirtualReleaseIdentity(a, b string) bool {
	if a == b {
		return true
	}
	aID, bID := virtualResultCandidateID(a), virtualResultCandidateID(b)
	return aID != "" && aID == bID && virtualReleaseComparisonKey(a) == virtualReleaseComparisonKey(b)
}

// virtualCandidateVerdictError reports a non-nil error when the concrete
// candidate URI is owned by a catalog row whose failed_at verdict is still
// active and the caller did not request an explicit retry. It is the single
// verdict gate used before a fallback resolution, again on the resolved
// identity, and once more after a probe completes, so every candidate — the
// requested pin, a resolved sibling, or a resolver-substituted release — is
// checked the same way.
//
// It fails closed. A lookup outage or an incomplete lookup result is reported
// as an error rather than silently treated as eligible, so a candidate cannot
// be resolved or adopted while its verdict is unknowable. The supplied row's
// own failure stamp is enforced independently of the lookup for its own
// release (the row may not be persisted yet); it never leaks onto a sibling.
func (h *PlaybackHandler) virtualCandidateVerdictError(ctx context.Context, candidateURI string, file *models.MediaFile, ownerID int, allowFailed bool) error {
	if h == nil || file == nil || candidateURI == "" || allowFailed {
		return nil
	}
	now := time.Now()
	if sameVirtualReleaseIdentity(file.FilePath, candidateURI) &&
		virtualCandidateVerdictActive(file.FailedAt, now) {
		return fmt.Errorf("%w: candidate %s is marked failed", ErrVirtualCandidateMarkedFailed, candidateURI)
	}
	row, found, lookupErr := h.lookupVirtualCandidateRowDetailed(ctx, candidateURI, file.ContentID, file.EpisodeID, ownerID)
	if lookupErr != nil {
		return fmt.Errorf("%w: candidate %s: %w", errVirtualCandidateVerdictUnknown, candidateURI, lookupErr)
	}
	if !found {
		// No catalog row owns the candidate: there is no verdict to enforce.
		return nil
	}
	// The row's verdict binds only the candidate its own row names: a failed
	// sibling row under the same neutral key (same episode/profile, different
	// ?result= pick) must never veto a different pick, and the neutral fallback
	// lookup returning the pinned row must not re-indict a sibling with the
	// pin's stamp. A renumbered dead release is tried once and re-indicted under
	// its new id instead.
	if virtualCandidateVerdictBindsRow(row, candidateURI) &&
		virtualCandidateVerdictActive(row.FailedAt, now) {
		return fmt.Errorf("%w: candidate %s is marked failed", ErrVirtualCandidateMarkedFailed, candidateURI)
	}
	return nil
}

// virtualCandidateVerdictBindsRow reports whether a failed catalog row's
// verdict binds a candidate URI. A verdict is evidence about the bytes its own
// row names, so it binds that candidate (the exact URI, or the same concrete
// result id under the same release key). It does not bind a different sibling
// pick under the same provider-neutral key: the neutral fallback lookup returns
// the session's pinned row (or the lowest-id row for the title), and binding its
// failed stamp to every sibling made a rotation walk reject every candidate
// with "is marked failed" without ever testing one. A renumbered same-release
// candidate is the identity rematch's job, not the verdict gate's; an unknown
// candidate is tried once and indicted under its own id if still dead.
func virtualCandidateVerdictBindsRow(row *models.MediaFile, candidateURI string) bool {
	if row == nil || candidateURI == "" {
		return false
	}
	return sameVirtualReleaseIdentity(row.FilePath, candidateURI)
}

// virtualCandidateRowVerified reports whether a catalog lookup result actually
// owns candidateURI. A positive row ID alone proves nothing: a partial row with
// a null or stale file_path must not be trusted as the candidate's owner, or
// verdict checks and background probe persistence would target a row that never
// matched the URI. A row that carries the exact candidate identity, or the
// provider-neutral identity without a concrete pick, is verified.
func virtualCandidateRowVerified(row *models.MediaFile, candidateURI string) bool {
	if row == nil || row.ID <= 0 || candidateURI == "" {
		return false
	}
	path := strings.TrimSpace(row.FilePath)
	if path == "" {
		return false
	}
	if sameVirtualReleaseIdentity(path, candidateURI) {
		return true
	}
	// The row owns the provider-neutral identity (no concrete ?result= pick):
	// that is the identity the neutral fallback lookup keys on, and that lookup
	// is profile-scoped. The profile is kept here: a 1080p neutral row must not
	// verify a 4K candidate the neutral lookup would never return. The same
	// release under a profile variant and the neutral key is recognized above by
	// the shared concrete result id, where the profile is only a qualifier.
	return virtualResultCandidateID(path) == "" &&
		virtualPlaybackNeutralKey(path) == virtualPlaybackNeutralKey(candidateURI)
}

// virtualCandidateRowOwnsNeutral reports whether a row returned by the
// provider-neutral lookup owns candidateURI's release. Unlike
// virtualCandidateRowVerified, it accepts a row that still carries a concrete
// ?result= pick: the provider rotates result ids for the same release, and the
// neutral lookup deliberately matches with result= stripped so that rotation is
// the same release. Its verdict still applies. The profile stays part of the
// neutral key, so a different profile variant remains a different release and
// is never accepted here.
//
// Only the fallback lookup may use this: the exact-path lookup is keyed on the
// concrete URI and a row it returns with a different path is genuinely
// untrustworthy, not a rotation.
func virtualCandidateRowOwnsNeutral(row *models.MediaFile, candidateURI string) bool {
	if row == nil || row.ID <= 0 || candidateURI == "" {
		return false
	}
	path := strings.TrimSpace(row.FilePath)
	if path == "" {
		return false
	}
	return virtualPlaybackNeutralKey(path) == virtualPlaybackNeutralKey(candidateURI)
}

// virtualProbeEvidenceRequiresAdoption reports whether probe evidence for
// candidateURI belongs to a different concrete release than the row it would be
// written to. It is exactly the negation of candidate ownership, so the two
// sides cannot drift:
//
//   - same-release (false): the row carries the candidate's exact release
//     identity, or the provider-neutral identity with no concrete pick. The
//     candidate's evidence is the row's own and stays metadata-only.
//   - cross-release (true): a different concrete pick under the same neutral
//     key, or a different neutral key. The row does not verifiably own those
//     bytes, so the write must be gated on a confirmed identity adoption.
//
// An empty candidateURI is not evidence for any release and returns false.
func virtualProbeEvidenceRequiresAdoption(row *models.MediaFile, candidateURI string) bool {
	if row == nil || candidateURI == "" {
		return false
	}
	return !virtualCandidateRowVerified(row, candidateURI)
}

// lookupVirtualCandidateRowDetailed resolves the catalog row that owns a
// concrete candidate URI and distinguishes a genuine not-found from a lookup
// failure or an incomplete row. found is false with a nil error only when no
// configured lookup knows the candidate; a lookup error or a non-nil row that
// does not verifiably own the candidate (no path, a stale path, or an ID with
// no usable identity) is returned as an error so the caller can fail closed.
//
// The exact-path lookup is authoritative: when it fails with a real lookup
// error, that error is preserved even if the provider-neutral fallback
// succeeds and returns a healthy row. The candidate's exact identity is the one
// whose verdict matters, and masking a failure to read it with a healthy
// fallback row would let a failed candidate be treated as eligible. The
// fallback row is still returned alongside the error so metadata-only callers
// can enrich from it; verdict callers inspect the error first and refuse.
//
// A genuine not-found is the exception: a row stored under the neutral key but
// requested with a concrete ?result= URI misses the exact lookup by design, so
// ErrVirtualCandidateNotFound (or scanner.ErrFileNotFound) does not taint the
// fallback row. An incomplete exact row still taints it, because that verdict
// is unknowable rather than absent.
func (h *PlaybackHandler) lookupVirtualCandidateRowDetailed(ctx context.Context, candidateURI, contentID, episodeID string, ownerID int) (*models.MediaFile, bool, error) {
	if h == nil || candidateURI == "" {
		return nil, false, nil
	}
	var exactErr, fallbackErr error
	exactIncomplete, fallbackIncomplete := false, false
	consider := func(row *models.MediaFile, err error, exact bool) (*models.MediaFile, bool) {
		if err != nil {
			if exact {
				if exactErr == nil {
					exactErr = err
				}
			} else if fallbackErr == nil {
				fallbackErr = err
			}
			return nil, false
		}
		if row == nil {
			return nil, false
		}
		verified := virtualCandidateRowVerified(row, candidateURI)
		if !verified && !exact {
			// The provider-neutral lookup matches the release with result=
			// stripped, so a stored row that still carries a different concrete
			// ?result= pick is a provider rotation of the same release, not an
			// incomplete row. Its verdict owns the candidate; treating it as
			// incomplete hard-failed rehydration for a row that exists.
			verified = virtualCandidateRowOwnsNeutral(row, candidateURI)
		}
		if verified {
			return row, true
		}
		// A non-nil row that does not verifiably own the candidate (no
		// identity, a null path, or a stale path) cannot carry a trustworthy
		// verdict; remember it and let the other lookup still own the
		// candidate. A positive ID alone is not ownership.
		if exact {
			exactIncomplete = true
		} else {
			fallbackIncomplete = true
		}
		return nil, false
	}
	if h.VirtualFileLookup != nil {
		exactRow, exactLookupErr := h.VirtualFileLookup(ctx, candidateURI)
		if row, found := consider(exactRow, exactLookupErr, true); found {
			// The exact identity is authoritative; the fallback is not
			// consulted and cannot override it.
			return row, true, nil
		}
	}
	if h.VirtualCandidateFileLookup != nil {
		fallbackRow, fallbackLookupErr := h.VirtualCandidateFileLookup(ctx, virtualPlaybackNeutralKey(candidateURI), contentID, episodeID, ownerID)
		if row, found := consider(fallbackRow, fallbackLookupErr, false); found {
			// A genuine not-found from the exact lookup is not a taint: the
			// fallback row is the one the provider-neutral key owns. A real
			// lookup error or an incomplete exact row still fails closed.
			if exactErr != nil && !isVirtualCandidateNotFound(exactErr) {
				return row, true, exactErr
			}
			if exactIncomplete {
				return row, true, errVirtualCandidateVerdictIncomplete
			}
			return row, true, nil
		}
	}
	if exactErr != nil && !isVirtualCandidateNotFound(exactErr) {
		return nil, false, exactErr
	}
	if fallbackErr != nil && !isVirtualCandidateNotFound(fallbackErr) {
		return nil, false, fallbackErr
	}
	if exactIncomplete || fallbackIncomplete {
		return nil, false, errVirtualCandidateVerdictIncomplete
	}
	// Both lookups reported a genuine not-found (or were not configured): no
	// catalog row owns the candidate, so there is no verdict to enforce.
	return nil, false, nil
}

// collectionVariantPinVanished reports whether a collection-owned variant row's
// pinned release is genuinely gone from the provider's fresh listing: the row
// carries a durable identity, and neither the pinned result id nor any listed
// candidate sharing that durable identity appears. A row without a durable
// identity never qualifies, because a renumbered listing is then
// indistinguishable from a vanished release and a swap would be silent. A row
// that is not collection-owned never qualifies either: this is the one
// ownership case whose path the collection sync otherwise keeps immutable.
func collectionVariantPinVanished(file *models.MediaFile, streams []VirtualPlaybackStream, pinID string) bool {
	if file == nil || file.ProbeSource != virtualCollectionProbeSource || pinID == "" {
		return false
	}
	identity, ok := persistedVirtualIdentity(file)
	if !ok {
		return false
	}
	for _, stream := range streams {
		if stream.ID == pinID || virtualResultCandidateID(stream.URI) == pinID {
			return false
		}
		if streamMatchesPersistedIdentity(stream, identity) {
			return false
		}
	}
	return true
}

// streamMatchesPersistedIdentity reports whether a listed stream belongs to the
// row's release, using the same durable-identity tier precedence as the
// resolver's dedup key. The provider release size is not a separate field on
// the stream record, so the candidate file size is used, exactly as the
// listing sink stores it.
func streamMatchesPersistedIdentity(stream VirtualPlaybackStream, identity virtuallibrary.PersistedCandidateIdentity) bool {
	want := resolver.PersistedDedupKey(identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize)
	got := resolver.PersistedDedupKey(stream.ProviderVideoHash, stream.ProviderGUID, stream.ProviderReleaseName, stream.FileSize)
	return want != "" && want == got
}

// virtualRecoveryRelistMax is the number of stale-source recovery re-lists one
// provider listing may perform inside virtualRecoveryRelistWindow. Both the
// stale fallback (which lists the provider directly) and the declared recovery
// bypasses that re-list past the floor (the alternate-version walk, its
// per-alternate prepare, and the replan rehydration) draw on this budget, so a
// provider whose listing keeps failing cannot have its fail-fast backoff
// bypassed on every start and recovery cannot poll it indefinitely. The bound
// is deliberately small: recovery exists to break a stale pin, not to keep a
// dead provider hot. A listing that answers with candidates clears the count;
// an empty answer does not, so a provider that keeps answering [] still
// accumulates toward the bound.
const virtualRecoveryRelistMax = 3

// virtualRecoveryRelistWindow matches the resolver's provider failure backoff:
// once exhausted, recovery stays on the floor for the same window the resolver
// would have suppressed the provider for anyway.
const virtualRecoveryRelistWindow = 30 * time.Second

// virtualRecoveryRelistMaxEntries caps the process-wide recovery damper. Live
// entries are bounded in practice by the providers failing inside one window.
const virtualRecoveryRelistMaxEntries = 4096

// virtualRecoveryRelistMark records how many recovery re-lists a provider
// listing has performed inside the current window.
type virtualRecoveryRelistMark struct {
	relists   int
	expiresAt time.Time
}

// virtualRecoveryRelistCache bounds stale-source recovery re-lists per provider
// listing. It is package-level because the handler is shared across requests; a
// mutex keeps concurrent starts safe and the map is pruned on write.
type virtualRecoveryRelistCache struct {
	mu    sync.Mutex
	marks map[string]virtualRecoveryRelistMark
	now   func() time.Time
}

func (c *virtualRecoveryRelistCache) clock() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

// allow records one recovery re-list for key and reports whether it is inside
// the bound. The first virtualRecoveryRelistMax relists in a window are
// admitted; a lapsed or absent window restarts the count. Denied calls do not
// extend the window, so a steady retry rate settles at the bound instead of
// holding recovery off forever.
func (c *virtualRecoveryRelistCache) allow(key string) bool {
	if c == nil || key == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	if c.marks == nil {
		c.marks = make(map[string]virtualRecoveryRelistMark)
	}
	mark, ok := c.marks[key]
	if ok && now.Before(mark.expiresAt) {
		if mark.relists >= virtualRecoveryRelistMax {
			return false
		}
		mark.relists++
		c.marks[key] = mark
		return true
	}
	if len(c.marks) >= virtualRecoveryRelistMaxEntries {
		for k, m := range c.marks {
			if !now.Before(m.expiresAt) {
				delete(c.marks, k)
			}
		}
	}
	c.marks[key] = virtualRecoveryRelistMark{relists: 1, expiresAt: now.Add(virtualRecoveryRelistWindow)}
	return true
}

// clear drops key's budget after a listing that answered, so a provider that
// recovered and later failed again starts from a full budget.
func (c *virtualRecoveryRelistCache) clear(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	delete(c.marks, key)
	c.mu.Unlock()
}

// virtualRecoveryRelists is the process-wide stale-source recovery damper.
// Tests may clear entries directly.
var virtualRecoveryRelists = &virtualRecoveryRelistCache{marks: make(map[string]virtualRecoveryRelistMark)}

// virtualEmptyListWindow suppresses a hot provider re-list after the provider
// just answered empty. E02/E03 showed full ~3.5s listings repeating every few
// seconds with count=0: each episode is a distinct resolver key, so the
// resolver's own 2m negative cache never shares, and the primary start-path
// list has no damper (only recovery paths consume virtualRecoveryRelists).
// This damper is start-path only and never blocks recovery or a forced relist.
const virtualEmptyListWindow = 30 * time.Second

// virtualEmptyListMarks remembers the last empty provider answer per listing:
// the neutral virtual path plus owning installation, same identity as
// virtualRecoveryRelistKey.
var virtualEmptyListMarks = struct {
	sync.Mutex
	marks map[string]time.Time
}{marks: make(map[string]time.Time)}

// virtualEmptyListSuppressed reports whether key answered empty inside the
// window. Expired entries are dropped on read so the map stays bounded.
func virtualEmptyListSuppressed(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	virtualEmptyListMarks.Lock()
	defer virtualEmptyListMarks.Unlock()
	last, ok := virtualEmptyListMarks.marks[key]
	if !ok {
		return false
	}
	if now.Sub(last) < virtualEmptyListWindow {
		return true
	}
	delete(virtualEmptyListMarks.marks, key)
	return false
}

// virtualEmptyListRecord remembers an empty answer at now.
func virtualEmptyListRecord(key string, now time.Time) {
	if key == "" {
		return
	}
	virtualEmptyListMarks.Lock()
	if len(virtualEmptyListMarks.marks) >= virtualRecoveryRelistMaxEntries {
		for k, t := range virtualEmptyListMarks.marks {
			if now.Sub(t) >= virtualEmptyListWindow {
				delete(virtualEmptyListMarks.marks, k)
			}
		}
	}
	virtualEmptyListMarks.marks[key] = now
	virtualEmptyListMarks.Unlock()
}

// virtualEmptyListClear drops the suppression after a listing answered with
// candidates, so a recovered provider is immediately listable again.
func virtualEmptyListClear(key string) {
	if key == "" {
		return
	}
	virtualEmptyListMarks.Lock()
	delete(virtualEmptyListMarks.marks, key)
	virtualEmptyListMarks.Unlock()
}

// virtualRecoveryRelistKey names one provider listing for the recovery damper:
// the provider-neutral virtual path plus the owning installation, matching the
// identity the stale fallback lists under.
func virtualRecoveryRelistKey(neutralKey string, ownerID int) string {
	return neutralKey + "\x00" + strconv.Itoa(ownerID)
}

// errVirtualCandidateExcluded reports that a resolve returned a candidate the
// durable recovery chain has indicted. Serving or adopting it would reopen the
// A→B→C→A cycle the chain exists to stop.
var errVirtualCandidateExcluded = errors.New("resolved candidate is excluded by the recovery chain")

// virtualCandidateIDExcluded reports whether candidateURI's concrete result id
// is in the exclusion chain. It is the free-function form used where only the
// slice is available (resolveVirtualCandidateSource), so a resolver that
// substitutes an excluded release is caught even when the caller already
// filtered the listed streams.
func virtualCandidateIDExcluded(candidateURI string, excluded []string) bool {
	id := virtualResultCandidateID(candidateURI)
	if id == "" {
		return false
	}
	for _, e := range excluded {
		if strings.TrimSpace(e) == id {
			return true
		}
	}
	return false
}

// virtualPathOwnerRow returns the catalog row that already owns a virtual
// candidate path within the same owner installation, library, content, and
// episode, when the handler's file resolver can look a row up by path. The
// stale fallback uses it to recognize a healthy substitute whose path belongs
// to an existing alternate-version row: that substitute must rotate the session
// to the owning row, not attempt an adoption the SQL sibling guard refuses. A
// resolver without GetByPath reports no owner, leaving ordinary adoption
// behavior.
//
// Ownership is asserted beyond the installation and library: the row must name
// the exact concrete path and carry this content's identity. A path collision
// alone is not ownership — a provider renumbers result ids per listing, and
// rotating to another content's or another episode's row would bind the session
// to bytes the viewer never selected.
//
// A lookup error is not safe absence. It returns an error so the caller fails
// closed instead of adopting a substitute whose owner is unknown. A nil row
// with a nil error is a genuine not-found and the only safe absence; a row
// without a usable id is treated as an unanswered lookup, not absence.
func (h *PlaybackHandler) virtualPathOwnerRow(ctx context.Context, file *models.MediaFile, candidateURI string, ownerID, libraryID int) (*models.MediaFile, error) {
	if h == nil {
		return nil, nil
	}
	return virtualPathOwnerRowFor(ctx, h.fileResolver, file, candidateURI, ownerID, libraryID)
}

// virtualPathOwnerRowFor is the resolver-parameterized form of
// virtualPathOwnerRow, so the StreamHandler serve path can apply the same
// ownership assertion the PlaybackHandler paths use.
func virtualPathOwnerRowFor(ctx context.Context, resolver FilePathResolver, file *models.MediaFile, candidateURI string, ownerID, libraryID int) (*models.MediaFile, error) {
	if file == nil || strings.TrimSpace(candidateURI) == "" {
		return nil, nil
	}
	row, capable, err := virtualPathLookupRowFor(ctx, resolver, candidateURI)
	if !capable || err != nil || row == nil {
		return nil, err
	}
	if row.VirtualOwnerInstallationID != ownerID || row.MediaFolderID != libraryID {
		return nil, nil
	}
	if file.ContentID == "" || row.ContentID != file.ContentID {
		return nil, nil
	}
	if row.EpisodeID != file.EpisodeID {
		return nil, nil
	}
	if strings.TrimSpace(row.FilePath) != strings.TrimSpace(candidateURI) {
		return nil, nil
	}
	return row, nil
}

// reanchorLateVirtualOwnerRow upgrades a live virtual session still pinned to a
// provider-neutral row onto the concrete ?result= owner row of its bound
// candidate, once that row appears. It returns the row to serve (the owner row
// when adopted, otherwise the input file) and the session as the caller should
// re-read it.
//
// A first-ever auto start resolves a concrete candidate before its catalog row
// exists, so the session keeps the release's neutral sibling; the deferred probe
// stamps that sibling and the session carries a verified overlay anchored to it.
// When the owner row materializes later in the same session, the serve and
// inventory binds resolve it by exact path, but the session's effective row and
// evidence anchor still name the neutral sibling, so the verified overlay is
// rejected as a different candidate and the menus freeze. Moving both onto the
// owner row keeps the overlay applying.
//
// The adoption is fully fenced: it only fires for an actually-neutral effective
// row (a row that already names any concrete ?result= pick is its own owner, or
// a different concrete release, and is never swapped), it captures the whole
// candidate binding before the owner-row lookup, and it CASes that snapshot -
// generation, virtual URI, owner, and effective row - in one critical section.
// The session manager also refuses the move while the deferred-probe lifecycle
// is pending, so a row-only move cannot strand an outstanding probe. A rejected
// CAS returns the input file: a rejected adoption is never served as if it had
// landed.
func reanchorLateVirtualOwnerRow(ctx context.Context, resolver FilePathResolver, sessionMgr SessionManagerInterface, file *models.MediaFile, session *playback.Session, candidateURI string) (*models.MediaFile, *playback.Session) {
	if file == nil || session == nil || sessionMgr == nil || !isVirtualPlaybackFile(file) {
		return file, session
	}
	candidateURI = strings.TrimSpace(candidateURI)
	if candidateURI == "" || virtualResultCandidateID(candidateURI) == "" {
		return file, session
	}
	// Only a genuinely neutral effective row is upgraded. A row that already
	// names a concrete ?result= pick is its own owner (or a different concrete
	// release); adopting another concrete row over it would silently swap the
	// release the viewer is bound to.
	if virtualResultCandidateID(file.FilePath) != "" {
		return file, session
	}
	bindingReader, ok := sessionMgr.(interface {
		VirtualSourceBinding(sessionID string) (playback.VirtualSourceBindingSnapshot, error)
	})
	if !ok {
		return file, session
	}
	// Capture the full binding before the owner-row lookup: the lookup is an
	// arbitrary catalog round trip, and a rotation that lands during it must make
	// the CAS reject rather than bless a stale adoption.
	binding, err := bindingReader.VirtualSourceBinding(session.ID)
	if err != nil {
		return file, session
	}
	if binding.EffectiveMediaFileID != file.ID || strings.TrimSpace(binding.VirtualURI) != candidateURI {
		// The row we hold is not the session's bound effective row, or the
		// binding already moved on from the candidate the caller named.
		return file, session
	}
	owner, err := virtualPathOwnerRowFor(ctx, resolver, file, candidateURI, file.VirtualOwnerInstallationID, file.MediaFolderID)
	if err != nil || owner == nil || owner.ID <= 0 || owner.ID == file.ID {
		return file, session
	}
	reanchorer, ok := sessionMgr.(interface {
		ReanchorVirtualEffectiveRow(sessionID string, expected playback.VirtualSourceBindingSnapshot, rowID int, evidenceURI string) (bool, error)
	})
	if !ok {
		return file, session
	}
	applied, err := reanchorer.ReanchorVirtualEffectiveRow(session.ID, binding, owner.ID, candidateURI)
	if err != nil || !applied {
		// A rejected CAS (a rotation, a row-only move, or a pending probe
		// lifecycle) means the session did not move: return the input file so a
		// rejected adoption is never served as if it had landed.
		return file, session
	}
	if refreshed, err := sessionMgr.GetSession(session.ID); err == nil && refreshed != nil {
		session = refreshed
	}
	return owner, session
}

// virtualPathLookupRow is the raw exact-path lookup behind virtualPathOwnerRow
// and the persist-path refusal classifier, so the ownership question and its
// diagnosis can never disagree about what a path resolves to.
//
// capable is false when the resolver cannot look rows up by path; the caller
// then cannot assert ownership and keeps its prior behavior. err is a
// fail-closed lookup failure: a real lookup error, or a non-nil row without a
// usable id (an unanswered lookup, not safe absence). A nil row with a nil
// error is a genuine not-found.
//
// A not-found sentinel (scanner.ErrFileNotFound or ErrVirtualCandidateNotFound)
// is a genuine absence, not an unanswered question: a row stores the neutral
// URI while a probed candidate carries a concrete ?result= pick, so the exact
// path lookup misses by design and must not be read as "ownership unknown". The
// caller then treats the path as unowned and proceeds (adopting or stamping the
// requested row) instead of refusing the write forever. Only a real read error
// stays fail-closed; the production resolver wraps GetByPath's not-found into
// ErrVirtualCandidateNotFound, and a bare scanner.ErrFileNotFound is equally
// absent.
func (h *PlaybackHandler) virtualPathLookupRow(ctx context.Context, candidateURI string) (row *models.MediaFile, capable bool, err error) {
	if h == nil {
		return nil, false, nil
	}
	return virtualPathLookupRowFor(ctx, h.fileResolver, candidateURI)
}

// virtualPathLookupRowFor is the resolver-parameterized form of
// virtualPathLookupRow.
func virtualPathLookupRowFor(ctx context.Context, resolver FilePathResolver, candidateURI string) (row *models.MediaFile, capable bool, err error) {
	if strings.TrimSpace(candidateURI) == "" {
		return nil, false, nil
	}
	pathResolver, ok := resolver.(interface {
		GetByPath(context.Context, string) (*models.MediaFile, error)
	})
	if !ok {
		return nil, false, nil
	}
	row, err = pathResolver.GetByPath(ctx, candidateURI)
	if err != nil {
		if isVirtualCandidateNotFound(err) {
			// No row owns the exact path: safe absence, not a lookup failure.
			return nil, true, nil
		}
		return nil, true, fmt.Errorf("virtual path owner lookup failed for %q: %w", candidateURI, err)
	}
	if row == nil {
		return nil, true, nil
	}
	if row.ID <= 0 {
		return nil, true, fmt.Errorf("virtual path owner lookup for %q returned an incomplete row", candidateURI)
	}
	return row, true, nil
}

// rotateVirtualSourceToOwnerRow returns the resolved source's file rebased on
// the existing alternate-version row the fallback rotates to, with the freshly
// probed track and stream evidence overlaid. Starting from the owning row keeps
// its catalog identity and state (id, content, provider identity, verdict);
// overlaying the probed evidence keeps the verified video/audio/subtitle tracks
// the resolver just produced. Replacing the probed file wholesale with the
// catalog row would strip that evidence while the source still reports
// ProbeProvenanceVerified, so the plan would be built on an empty inventory.
func rotateVirtualSourceToOwnerRow(probed *models.MediaFile, ownerRow *models.MediaFile, uri string) *models.MediaFile {
	if ownerRow == nil {
		return probed
	}
	rotated := *ownerRow
	rotated.FilePath = uri
	if probed != nil {
		rotated.VideoTracks = probed.VideoTracks
		rotated.AudioTracks = probed.AudioTracks
		rotated.SubtitleTracks = probed.SubtitleTracks
		rotated.ExternalSubtitles = probed.ExternalSubtitles
		rotated.Chapters = probed.Chapters
		rotated.Resolution = probed.Resolution
		rotated.CodecVideo = probed.CodecVideo
		rotated.CodecAudio = probed.CodecAudio
		rotated.Container = probed.Container
		rotated.HDR = probed.HDR
		rotated.Bitrate = probed.Bitrate
		rotated.Duration = probed.Duration
		rotated.AudioChannels = probed.AudioChannels
		rotated.FileSize = probed.FileSize
		rotated.MultiplePPS = probed.MultiplePPS
		rotated.MultiplePPSScanSize = probed.MultiplePPSScanSize
		rotated.MultiplePPSScanMtime = probed.MultiplePPSScanMtime
	}
	return &rotated
}

// virtualRequestedRowIsSelection reports whether the requested row is a
// concrete version the viewer can select rather than a provider-neutral
// placeholder that exists only to own a release's candidates. A placeholder
// has no own candidate, probe stamp, or track inventory, so a plan that binds
// it to a concrete owner is ordinary auto selection, not a substitution of the
// viewer's version.
func virtualRequestedRowIsSelection(row *models.MediaFile) bool {
	if row == nil {
		return false
	}
	return virtualResultCandidateID(row.FilePath) != "" ||
		row.ProbeUpdatedAt != nil ||
		len(row.AudioTracks) > 0 ||
		len(row.SubtitleTracks) > 0
}

// virtualRotationWouldDropTracks reports whether adopting target as the
// effective row would offer fewer audio or subtitle tracks than the selected
// row. It is the track-safety half of the rotation rule: adopting the identity
// of the bytes' owner is allowed, losing a track the viewer selected is not.
//
// probed is the inventory the plan will actually carry after the rotation
// (rotateVirtualSourceToOwnerRow overlays it on the owner row), so a measured
// probe is compared instead of the owner's stale declaration. A non-nil probed
// file with zero tracks is a measurement, not missing evidence. When probed is
// nil the target row's declaration is the best available inventory; a row with
// no tracks at all is treated as unknown, never as zero, so an unprobed owner
// is not rejected for the absence of a declaration.
func virtualRotationWouldDropTracks(selected, target, probed *models.MediaFile) bool {
	if selected == nil {
		return false
	}
	selectedAudio, selectedSubtitles := len(selected.AudioTracks), len(selected.SubtitleTracks)
	if selectedAudio == 0 && selectedSubtitles == 0 {
		// A placeholder selects no tracks; there is nothing to lose.
		return false
	}
	if probed != nil {
		return len(probed.AudioTracks) < selectedAudio || len(probed.SubtitleTracks) < selectedSubtitles
	}
	if target == nil {
		return false
	}
	if len(target.AudioTracks) == 0 && len(target.SubtitleTracks) == 0 {
		return false
	}
	return len(target.AudioTracks) < selectedAudio || len(target.SubtitleTracks) < selectedSubtitles
}

// virtualCompletedReleaseRetryBudget bounds the one same-release retry a
// completed, duration-verified pinned release gets before the alternate walk.
// It matches the detached catalog discovery budget: a provider that just
// answered slowly enough to time the first resolve out gets room to answer once
// more, but a dead release cannot hold the start indefinitely.
const virtualCompletedReleaseRetryBudget = 20 * time.Second

// virtualReleaseCompletedAndVerified reports whether the pinned row is a release
// the provider already holds and whose measured duration proves the item. Such a
// release is playable; a transport hiccup on it is a transport problem, not
// evidence that a different release should be served.
//
// Completion comes from the provider's cache-completion signal when it is wired
// (AltMount's once-per-release confirmation, the same predicate the serve layer
// uses for cache handoff). Without that signal, a complete probe inventory is
// the strongest evidence the catalog row carries that the bytes were fetched and
// read. Duration verification requires a probe stamp: a declaration-only
// duration is the catalog's runtime, not a measurement, and cannot prove
// identity.
func (h *PlaybackHandler) virtualReleaseCompletedAndVerified(ctx context.Context, file *models.MediaFile) bool {
	if h == nil || file == nil || file.ProbeUpdatedAt == nil {
		return false
	}
	if !completeVirtualVideoEvidenceV3(file) || !completeVirtualAudioEvidenceV3(file) ||
		!completeVirtualContainerEvidenceV3(file) {
		return false
	}
	expectedMinutes := h.virtualExpectedRuntimeMinutes(ctx, file)
	if expectedMinutes == 0 {
		expectedMinutes = file.VirtualExpectedRuntimeMinutes
	}
	if !scanner.VirtualDurationProvesIdentity(file.Duration, expectedMinutes) {
		return false
	}
	if h.VirtualReleaseCacheStatus != nil {
		if cached, known := h.VirtualReleaseCacheStatus(ctx, file.FilePath, effectiveVirtualOwner(0, file.VirtualOwnerInstallationID)); known {
			return cached
		}
	}
	return true
}

// virtualCompletedReleaseResolveHiccup reports whether a failed resolve of the
// pinned release may be retried as the same release. Only transient dependency
// failures qualify; a release verdict — an absent pin, a marked-failed candidate,
// an unreadable verdict — is confirmed dead and rotates as before. Pending
// imports are handled by the caller's own hold and are not retried here.
func virtualCompletedReleaseResolveHiccup(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, virtuallibrary.ErrProviderPending) ||
		errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) ||
		errors.Is(err, ErrVirtualCandidateMarkedFailed) ||
		errors.Is(err, errVirtualCandidateVerdictUnknown) {
		return false
	}
	return virtualProviderListingOutage(err) ||
		errors.Is(err, errVirtualProviderUnavailable) ||
		errors.Is(err, context.DeadlineExceeded) ||
		playback.IsTransientProviderError(err)
}

// retryCompletedPinnedReleaseV3 re-resolves exactly the pinned release once with
// a fresh relay registration, so a transport hiccup on a completed release gets
// new bytes before the caller walks to a different release. The retry is forced
// through a provider re-list (bypassing the negative cache the failure just
// wrote), and its result is accepted only when it names the same release: a
// provider renumber re-matches by durable identity, but a ranked sibling is not
// a retry and is refused. A failure or a different release returns an error and
// leaves the caller's existing alternate walk in charge.
func (h *PlaybackHandler) retryCompletedPinnedReleaseV3(
	ctx context.Context,
	file *models.MediaFile,
	ownerInstallationID, userID int,
	profileID, pinnedURI, preferredCandidateID, pinnedCandidateID string,
	budget time.Duration,
) (ResolvedVirtualMedia, func(), error) {
	if budget <= 0 || budget > virtualCompletedReleaseRetryBudget {
		budget = virtualCompletedReleaseRetryBudget
	}
	retryCtx, cancel := context.WithTimeout(withVirtualRelayFreshRegistration(ctx), budget)
	defer cancel()
	resolved, cleanup, err := h.resolveVirtualInputURI(
		retryCtx, pinnedURI, ownerInstallationID, userID, profileID, true, nil, preferredCandidateID,
	)
	if err != nil {
		return ResolvedVirtualMedia{}, nil, err
	}
	sameRelease := (pinnedCandidateID != "" && resolved.CandidateID == pinnedCandidateID) ||
		resolved.IdentityRematched || resolvedMatchesPersistedIdentity(resolved, file)
	if !sameRelease {
		if cleanup != nil {
			cleanup()
		}
		return ResolvedVirtualMedia{}, nil, fmt.Errorf(
			"same-release retry resolved candidate %q for pinned candidate %q",
			resolved.CandidateID, pinnedCandidateID)
	}
	slog.InfoContext(ctx, "virtual playback transport hiccup on a completed release; retried the same release with a fresh relay registration",
		"component", "api", "file_id", catalogFileIDOrZero(file),
		"candidate_id", pinnedCandidateID, "virtual_uri", pinnedURI)
	return resolved, cleanup, nil
}

// fallbackResolveStaleVirtualSource re-lists the provider's current candidates
// and resolves the first healthy provider-neutral stream. It returns nil when
// the original URI carried no stale result= pick, or when no substitute
// candidate can be resolved, so the caller preserves its original error.
//
// elig is the explicit release-identity contract built by the caller from the
// resolve intent (session binding + explicit rotation) and the anchored
// release. Under a session binding the fallback refuses to swap the release
// unless rotation was declared: it first re-resolves the session's own
// candidate (reusing the same release), and otherwise returns nil so the caller
// surfaces the original failure. A sibling is only tried when rotation was
// declared or the resolve is not session-bound, and the same check is enforced
// again before a replacement is persisted, so there is no path that resolves or
// adopts a sibling outside the declared intent.
func (h *PlaybackHandler) fallbackResolveStaleVirtualSource(
	ctx context.Context,
	file *models.MediaFile,
	userID int,
	profileID string,
	elig virtualFallbackEligibility,
) *resolvedVirtualPlaybackSource {
	parsed, _ := url.Parse(file.FilePath)
	if parsed != nil && strings.TrimSpace(parsed.Query().Get("result")) == "" {
		return nil
	}
	// The session anchor is immutable. A session-bound request must refresh or
	// rotate the release the session is actually serving, not whatever the
	// persisted catalog row's file_path has drifted to. Validate the row-derived
	// identity against the anchor and refuse on a mismatch before listing,
	// resolving or persisting anything, so a drift cannot be silently adopted
	// under the session's name.
	if elig.sessionBound && elig.releaseID != "" {
		if got := virtualResultCandidateID(file.FilePath); got != elig.releaseID {
			slog.ErrorContext(ctx, "virtual stale fallback: persisted row does not match the session anchor; refusing",
				"component", "api", "file_id", file.ID, "persisted_release", got, "session_anchor_release", elig.releaseID)
			return nil
		}
	}
	if h.VirtualPlaybackStreamLister == nil {
		return nil
	}
	neutralKey := virtualPlaybackNeutralKey(file.FilePath)
	// Bound repeated recovery re-lists per provider listing. The fallback lists
	// the provider directly, so a provider whose listing keeps failing would
	// otherwise have the resolver's provider fail-fast backoff bypassed on every
	// start. A listing that answers with candidates clears the budget below, so
	// only genuine listing failures and empty answers accumulate; once
	// exhausted the fallback surfaces the transient provider cause so the
	// caller's bounded alternate walk can still reach a healthy sibling.
	recoveryKey := virtualRecoveryRelistKey(neutralKey, file.VirtualOwnerInstallationID)
	if !virtualRecoveryRelists.allow(recoveryKey) {
		slog.WarnContext(ctx, "virtual stale fallback: recovery re-list budget exhausted; deferring to the bounded alternate walk",
			"component", "api", "file_id", file.ID, "neutral_key", neutralKey)
		// The provider has failed enough listings that recovery must stop
		// re-listing it, but the pinned release must not be abandoned silently:
		// surface the transient provider cause so the caller's bounded
		// alternate-version walk can still reach a healthy sibling on another
		// provider instead of leaving the viewer pinned to the dead release for
		// the rest of the window.
		if elig.anchorErr != nil {
			*elig.anchorErr = fmt.Errorf("%w: stale-source recovery re-list budget exhausted", resolver.ErrProviderUnavailable)
		}
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	streams, listErr := h.VirtualPlaybackStreamLister.ListVirtualPlaybackStreams(
		listCtx, neutralKey, userID, profileID, file.VirtualOwnerInstallationID,
	)
	cancel()
	if listErr != nil {
		slog.ErrorContext(ctx, "virtual stale fallback: list failed", "component", "api", "neutral_key", neutralKey, "error", listErr)
		return nil
	}
	if len(streams) == 0 {
		// An empty answer is a provider hiccup, not a verdict: it must not
		// clear the budget, so a provider that keeps answering [] still
		// accumulates toward the bound instead of being re-listed on every
		// press.
		slog.ErrorContext(ctx, "virtual stale fallback: no streams listed", "component", "api", "neutral_key", neutralKey)
		return nil
	}
	// The provider answered with candidates, so recovery is no longer defeating
	// a provider backoff; a later failure starts from a full budget.
	virtualRecoveryRelists.clear(recoveryKey)
	if len(streams) > maxVirtualPlaybackStreams {
		streams = streams[:maxVirtualPlaybackStreams]
	}
	// Guard against cross-identity candidates: only consider streams that
	// share the same scheme, host, path, and profile as the original file.
	streams = filterVirtualPlaybackStreams(file, streams)

	// The session-bound release is the one the viewer chose. A stale or dead
	// session candidate must not be silently replaced by a sibling release:
	// only an explicit candidate rotation may substitute. The intent arrives
	// as the eligibility contract, never inferred from the candidate list.
	// When the eligibility carries a validated session anchor id it is
	// authoritative; otherwise the row-derived id is used.
	sessionID := virtualResultCandidateID(file.FilePath)
	if elig.sessionBound && elig.releaseID != "" {
		sessionID = elig.releaseID
	}
	// A collection-owned variant row whose pinned release genuinely vanished
	// from the fresh listing may reconcile to the profile's live candidate.
	// This is the one case a session-bound request may substitute a different
	// release without an explicit rotation: the row is the viewer's selection
	// and its release no longer exists. A still-listed release — the same
	// result id, or the same durable identity under a renumbered id — never
	// qualifies, so a live release is never silently exchanged.
	reconcileCollection := collectionVariantPinVanished(file, streams, sessionID)
	if !elig.allowsSibling() {
		if sessionID == "" && !reconcileCollection {
			// No anchored release to refresh and siblings are forbidden.
			return nil
		}
		if sessionID != "" {
			// Prefer the persisted same-identity candidate: when the caller's
			// own row still carries a usable stored URL for this exact release,
			// serve it instead of listing again. The row's release identity was
			// validated against the session anchor above and
			// evaluateStoredVirtualURLCandidate re-checks it, so this is never a
			// sibling, and a replay keeps working even when the provider's
			// re-list no longer contains the candidate.
			if usable, state := evaluateStoredVirtualURLCandidate(
				ctx, file.FilePath, file,
				h.storedVirtualURLAllowInsecure(file, file.VirtualOwnerInstallationID),
				time.Now(), h.virtualCandidateTrustWindow(),
			); state == virtualStoredURLUsable {
				ownerID := effectiveVirtualOwner(usable.OwnerID, file.VirtualOwnerInstallationID)
				transient := *file
				transient.FilePath = usable.URI
				transient.VirtualOwnerInstallationID = ownerID
				var expiresAt *time.Time
				if !usable.ExpiresAt.IsZero() {
					expiry := usable.ExpiresAt
					expiresAt = &expiry
				}
				slog.InfoContext(ctx, "virtual stale fallback: serving the persisted session-bound candidate",
					"component", "api", "original", file.FilePath, "status", "persisted_candidate")
				return &resolvedVirtualPlaybackSource{
					URL: usable.URL, URI: usable.URI, OwnerID: ownerID, File: &transient,
					ResolvedURL: usable.URL, ResolvedURLExpiresAt: expiresAt,
					RequestHeaders: cloneHeaderMap(usable.RequestHeaders),
					Provenance:     ProbeProvenanceDeclared,
				}
			}
			sessionCandidate := VirtualPlaybackStream{
				ID:                  sessionID,
				URI:                 file.FilePath,
				OwnerInstallationID: file.VirtualOwnerInstallationID,
				Resolution:          file.Resolution,
				CodecVideo:          file.CodecVideo,
				CodecAudio:          file.CodecAudio,
				HDR:                 mediaFileHDRString(file),
			}
			resolved, err := h.resolveVirtualCandidateSource(ctx, file, sessionCandidate, userID, profileID, elig.allowFailed, elig.excludedCandidateIDs)
			switch {
			case err == nil && (sessionID == "" || virtualResultCandidateID(resolved.URI) == sessionID):
				// Reuse the session's own resolved URL: re-resolving the chosen
				// candidate refreshes stale credentials without changing the
				// bytes under the viewer.
				slog.InfoContext(ctx, "virtual stale fallback: re-resolved the session-bound candidate",
					"component", "api", "original", file.FilePath)
				return resolved
			case err == nil:
				// The resolver returned a different result= identity (a dedup
				// keeper or a ranked sibling); serving it would swap the release.
				slog.WarnContext(ctx, "virtual stale fallback: refusing to substitute a different release",
					"component", "api", "original", file.FilePath, "resolved", resolved.URI,
					"reason", "candidate rotation was not requested")
			default:
				if elig.anchorErr != nil {
					*elig.anchorErr = err
				}
				slog.WarnContext(ctx, "virtual stale fallback: refusing to substitute a different release",
					"component", "api", "original", file.FilePath,
					"reason", "the session-bound candidate did not resolve and candidate rotation was not requested",
					"error", err)
			}
		}
		if !reconcileCollection {
			return nil
		}
	}

	maxAttempts := h.maxVirtualFailoverAttempts(ctx)
	attempts := 0
	for _, stream := range streams {
		if stream.URI == "" || stream.URI == file.FilePath {
			continue
		}
		// A confirmed exclusion is absolute: a release an earlier hop already
		// indicted must never be served or adopted by the stale fallback, or
		// the re-list would reopen the A→B→C→A cycle the durable chain exists
		// to stop.
		if elig.candidateExcluded(virtualResultCandidateID(stream.URI)) {
			continue
		}
		// Re-enforce the release-identity contract at the point of deciding to
		// contact a sibling; a caller cannot reach the loop with a forbidden
		// intent, but the check is the invariant, not the branch above. The one
		// permitted exception is a collection variant whose pinned release
		// proved absent, which carries its own recorded verdict.
		if !elig.allowsSibling() && !reconcileCollection {
			return nil
		}
		// A canceled request must stop the walk, not fail every remaining
		// sibling: each resolve errors instantly on the dead context and the
		// per-sibling lines below would turn one abandoned client into an
		// ERROR storm.
		if ctx.Err() != nil {
			slog.InfoContext(ctx, "virtual stale fallback: walk abandoned, client gone",
				"component", "api", "file_id", file.ID)
			return nil
		}
		attempts++
		if attempts > maxAttempts {
			break
		}
		resolved, err := h.resolveVirtualCandidateSource(ctx, file, stream, userID, profileID, elig.allowFailed, elig.excludedCandidateIDs)
		if err == nil {
			// Validate the substitute the resolver actually returned, not just
			// the listed stream. A dedup keeper or a fresh-selection
			// fall-through can substitute a release the durable chain has
			// already indicted; serving or adopting it would reopen the
			// A→B→C→A cycle the chain exists to stop.
			if virtualCandidateIDExcluded(resolved.URI, elig.excludedCandidateIDs) {
				slog.WarnContext(ctx, "virtual stale fallback: refusing a substitute excluded by the recovery chain",
					"component", "api", "file_id", file.ID, "candidate", stream.URI, "resolved", resolved.URI,
					"candidate_id", virtualResultCandidateID(resolved.URI))
				continue
			}
			slog.InfoContext(ctx, "virtual stale fallback: resolved substitute", "component", "api", "original", file.FilePath, "substitute", stream.URI)
			// Persist the substitute's validated identity and probed metadata
			// back to the catalog row in a single CAS-fenced save, replacing the
			// stale result= URI the next start would have to re-list. The
			// identity contract is enforced once more here: a session-bound
			// request must never adopt a replacement it was not allowed to
			// resolve. A collection-variant reconcile carries its own recorded
			// verdict, set only when the pinned release proved absent.
			if !elig.allowsSibling() && !reconcileCollection {
				return nil
			}
			// A substitute whose path is already owned by another row of this
			// content is an existing alternate version, not a release to fold
			// into this row: the CAS-fenced adoption below would collide with
			// the sibling owner and be refused, which is exactly the dead-pin
			// livelock this fallback exists to break. Rotate to the owning row
			// instead — the session binds to the version that actually plays,
			// and no catalog write is needed because the row already carries
			// the identity. The AltMount veto is enforced upstream in
			// resolveVirtualCandidateSource: an active failed_at on the
			// requested or the resolved candidate refuses before this point.
			if ownerRow, ownerErr := h.virtualPathOwnerRow(ctx, file, resolved.URI, effectiveVirtualOwner(stream.OwnerInstallationID, file.VirtualOwnerInstallationID), file.MediaFolderID); ownerErr != nil {
				// The ownership question could not be answered: an unanswered
				// lookup is not safe absence, so refuse the substitute rather
				// than adopt a path whose owner is unknown.
				slog.ErrorContext(ctx, "virtual stale fallback: refusing to substitute while the path owner is unknown",
					"component", "api", "file_id", file.ID, "candidate", stream.URI, "resolved", resolved.URI, "error", ownerErr)
				return nil
			} else if ownerRow != nil && ownerRow.ID != file.ID {
				// A substitute whose path is already owned by another row of
				// this content is an existing alternate version, not a release
				// to fold into this row: the CAS-fenced adoption below would
				// collide with the sibling owner and be refused, which is
				// exactly the dead-pin livelock this fallback exists to break.
				// Rotate to the owning row instead — the session binds to the
				// version that actually plays, and no catalog write is needed
				// because the row already carries the identity. The AltMount
				// veto is enforced upstream in resolveVirtualCandidateSource:
				// an active failed_at on the requested or the resolved
				// candidate refuses before this point. The owning row's catalog
				// identity is kept and the freshly probed track/stream evidence
				// is overlaid so the verified inventory is not lost.
				//
				// Track safety: a substitute that would offer fewer audio or
				// subtitle tracks than the selected row is not a rescue, it is
				// a silent downgrade. Refuse it and let the walk try another
				// candidate; if none survives, the start fails honestly.
				if virtualRotationWouldDropTracks(file, ownerRow, resolved.File) {
					slog.WarnContext(ctx, "virtual stale fallback: refusing a substitute that would lose audio or subtitle tracks",
						"component", "api", "file_id", file.ID, "substitute_file_id", ownerRow.ID,
						"candidate", stream.URI, "resolved", resolved.URI,
						"selected_audio_tracks", len(file.AudioTracks), "selected_subtitle_tracks", len(file.SubtitleTracks),
						"substitute_audio_tracks", len(resolved.File.AudioTracks), "substitute_subtitle_tracks", len(resolved.File.SubtitleTracks))
					continue
				}
				rotated := *resolved
				rotated.File = rotateVirtualSourceToOwnerRow(resolved.File, ownerRow, resolved.URI)
				slog.InfoContext(ctx, "virtual stale fallback: rotated to an existing alternate version row",
					"component", "api", "original", file.FilePath, "substitute", resolved.URI,
					"original_file_id", file.ID, "substitute_file_id", ownerRow.ID)
				return &rotated
			}
			if resolved.File != nil && resolved.Provenance == ProbeProvenanceVerified && !resolved.EvidenceObservation && h.VirtualFileSaver != nil {
				// Persist the identity the resolver actually returned
				// (resolved.URI), not the URI that was requested. A dedup
				// keeper or a fresh-selection fall-through legitimately
				// substitutes a different release, and the probed inventory
				// belongs to the returned bytes; adopting the requested URI
				// would pin a row that never owned that inventory.
				if resolved.URI == "" {
					slog.ErrorContext(ctx, "virtual stale fallback: refusing to adopt an unknown resolved identity",
						"component", "api", "file_id", file.ID, "candidate", stream.URI)
					return nil
				}
				// The verdict check that validated this candidate ran in
				// resolveVirtualCandidateSource, before this write. A failure
				// committed in between must still block adoption: the save
				// below carries the validated identity and the saver fences the
				// write against a live failed_at verdict in the same statement.
				// The barrier hook exists so a test can commit that failure in
				// exactly this window.
				if virtualAdoptionBarrier != nil {
					virtualAdoptionBarrier()
				}
				snap := snapshotVirtualRow(file)
				rows, saveErr := h.VirtualFileSaver(ctx, models.VirtualFilePersistArgs{
					FileID:           snap.FileID,
					ExpectedFilePath: file.FilePath,
					VideoTracks:      marshalTracksJSON(sanitizeTrackSlice(resolved.File.VideoTracks)),
					AudioTracks:      marshalTracksJSON(sanitizeTrackSlice(resolved.File.AudioTracks)),
					SubtitleTracks:   marshalTracksJSON(sanitizeTrackSlice(resolved.File.SubtitleTracks)),
					Resolution:       resolved.File.Resolution,
					CodecVideo:       resolved.File.CodecVideo,
					CodecAudio:       resolved.File.CodecAudio,
					Container:        resolved.File.Container,
					HDR:              resolved.File.HDR,
					Bitrate:          resolved.File.Bitrate,
					Duration:         resolved.File.Duration,
					StampProbe:       true,
					UpdatedAt:        snap.UpdatedAt,
					ProbeUpdatedAt:   snap.ProbeUpdatedAt,
					// The row being left is a different concrete release, so the
					// identity gate must replace its transport set rather than
					// preserve any omitted tier under the substitute's path.
					ExpectedProviderVideoHash:   file.ProviderVideoHash,
					ExpectedProviderGUID:        file.ProviderGUID,
					ExpectedProviderReleaseName: file.ProviderReleaseName,
					ExpectedProviderReleaseSize: file.ProviderReleaseSize,
					OwnerID:                     snap.OwnerID,
					LibraryID:                   snap.LibraryID,
					AdoptPath:                   resolved.URI,
					// Persist the provider URL and durable identity the
					// resolution produced alongside the adopted identity.
					ResolvedURL:            resolved.ResolvedURL,
					ResolvedURLExpiresAt:   resolved.ResolvedURLExpiresAt,
					ProviderVideoHash:      resolved.ProviderVideoHash,
					ProviderGUID:           resolved.ProviderGUID,
					ProviderReleaseName:    resolved.ProviderReleaseName,
					ProviderReleaseSize:    resolved.ProviderReleaseSize,
					ProviderRequestHeaders: cloneHeaderMap(resolved.RequestHeaders),
					// The fallback reports a substitute identity to the
					// session, so metadata alone is not enough: the saver must
					// confirm the validated identity was actually adopted.
					RequireAdopt: true,
					// An explicit retry deliberately re-adopts a known-bad
					// candidate, so the verdict fence must not block it.
					AllowFailedVerdict: elig.allowFailed,
					// The recorded verdict that this collection-owned row's pin
					// vanished and the fresh candidate may take its path. False
					// for every ordinary substitution, so a collection path is
					// otherwise immutable.
					ReconcileCollectionVariant: reconcileCollection,
				})
				if saveErr != nil {
					if errors.Is(saveErr, errVirtualAdoptIdentityNotPersisted) {
						// Metadata may still have landed, but the validated
						// identity did not: reporting the substitute would
						// serve bytes the catalog row does not own. Refuse and
						// let the next start re-list.
						slog.WarnContext(ctx, "virtual stale fallback: substitute identity was not adopted",
							"component", "api", "file_id", file.ID,
							"expected_path", file.FilePath, "adopt_path", resolved.URI, "error", saveErr)
						return nil
					}
					slog.ErrorContext(ctx, "virtual stale fallback: persist failed", "component", "api", "file_id", file.ID, "error", saveErr)
					return nil
				}
				if rows == 0 {
					// The CAS fence (file_path/updated_at/probe stamp/owner/
					// library) rejected the write: the row changed under us
					// after the snapshot, so the row did not adopt this
					// substitute. Report the fallback as failed rather than
					// serving a substitute the catalog never accepted; the
					// caller preserves its original error and the next start
					// re-lists.
					slog.WarnContext(ctx, "virtual stale fallback: persist CAS miss; substitute not adopted",
						"component", "api", "file_id", file.ID,
						"expected_path", file.FilePath, "adopt_path", resolved.URI)
					return nil
				}
			}
			return resolved
		}
		if errors.Is(err, errVirtualCandidateVerdictUnknown) {
			// The catalog could not answer for this sibling. Continuing would
			// try other siblings while the catalog is unhealthy, and treating
			// the unknown verdict as a dead candidate could substitute one the
			// verdict system never cleared. Fail the whole fallback closed.
			// A canceled scan is the client going away, not a server fault:
			// do not page on it.
			level := slog.LevelError
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				level = slog.LevelWarn
			}
			slog.Log(ctx, level, "virtual stale fallback: refusing to substitute while the candidate verdict is unknown",
				"component", "api", "candidate", stream.URI, "error", err)
			return nil
		}
		level := slog.LevelError
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "virtual stale fallback: candidate failed", "component", "api", "candidate", stream.URI, "error", err)
	}
	return nil
}

// resolveVirtualCandidateSource resolves and probes a single virtual stream
// candidate, returning a fully-probed source on success.
//
// excludedCandidateIDs is the durable recovery chain. It is threaded into the
// detailed resolver so a pin whose variant dedup collapsed or a fresh-selection
// fall-through cannot substitute an already-indicted release, and the resolved
// identity is validated against it again here, because the resolver may return
// a substitute the caller's listed-stream filter never saw.
func (h *PlaybackHandler) resolveVirtualCandidateSource(
	ctx context.Context,
	file *models.MediaFile,
	candidate VirtualPlaybackStream,
	userID int,
	profileID string,
	allowFailed bool,
	excludedCandidateIDs []string,
) (*resolvedVirtualPlaybackSource, error) {
	ownerID := candidate.OwnerInstallationID
	if ownerID <= 0 {
		ownerID = file.VirtualOwnerInstallationID
	}
	// This resolves one specific candidate for the stale-source fallback; the
	// caller loop provides substitution by trying the next listed stream. It is
	// therefore session-bound: a profile-removed candidate is reported as a
	// failure for that stream rather than silently resolving a different one
	// while the caller persists this stream's URI.
	ctx = withVirtualSessionBindingV3(ctx, true)
	// An excluded candidate must never be contacted or served, matching the
	// caller's listed-stream filter. This is the narrow check for a candidate
	// the caller did not already filter (the session's own re-resolve).
	if virtualCandidateIDExcluded(candidate.URI, excludedCandidateIDs) {
		return nil, fmt.Errorf("%w: candidate %q", errVirtualCandidateExcluded, virtualResultCandidateID(candidate.URI))
	}
	// A stale-source fallback must never hand back a candidate the serve layer
	// already marked failed. Check the candidate before contacting the provider
	// and again on the identity the resolver actually returned, because a
	// fresh-selection fall-through or a dedup keeper can substitute a sibling
	// whose row carries an active verdict. The explicit retry policy
	// (allowFailed) is the only bypass.
	if err := h.virtualCandidateVerdictError(ctx, candidate.URI, file, ownerID, allowFailed); err != nil {
		return nil, err
	}
	var streamURL string
	var resolvedExpiresAt *time.Time
	var providerVideoHash, providerGUID, providerReleaseName string
	var providerReleaseSize int64
	var identityRematched bool
	if h.VirtualMediaDetailedResolver != nil {
		// When this candidate is the persisted row's own release, thread the
		// durable identity and trust window so an absent same-identity candidate
		// inside the window refuses with ErrPersistedCandidateTrusted instead of
		// the rotation sentinel. A sibling carries no such identity: threading
		// the anchor's identity into a sibling resolve could rematch back to the
		// anchor.
		candidateCtx := ctx
		if sameVirtualReleaseIdentity(file.FilePath, candidate.URI) {
			candidateCtx = virtualResolveContextWithPersistedTrust(
				ctx, file, time.Now(), h.virtualCandidateTrustWindow(), h.storedVirtualURLAllowInsecure(file, ownerID),
			)
		}
		res, err := h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(
			candidateCtx, candidate.URI, ownerID, userID, profileID, false, excludedCandidateIDs, "",
		)
		if err != nil {
			return nil, err
		}
		streamURL = res.URL
		if !res.ExpiresAt.IsZero() {
			expiresAt := res.ExpiresAt
			resolvedExpiresAt = &expiresAt
		}
		providerVideoHash = res.ProviderVideoHash
		providerGUID = res.ProviderGUID
		providerReleaseName = res.ProviderReleaseName
		providerReleaseSize = res.ProviderReleaseSize
		identityRematched = res.IdentityRematched
		candidate.RequestHeaders = cloneHeaderMap(res.RequestHeaders)
		if res.URI != "" {
			candidate.URI = res.URI
		}
	} else if h.VirtualPlaybackResolver != nil {
		var err error
		streamURL, err = h.VirtualPlaybackResolver.ResolveVirtualPlayback(
			ctx, candidate.URI, userID, profileID, ownerID,
		)
		if err != nil {
			return nil, err
		}
		candidate.RequestHeaders = nil
	} else {
		return nil, errors.New("virtual playback resolver is not configured")
	}
	// The resolver may have substituted a different release. The exclusion and
	// the verdict are both part of that release's identity, so re-check the
	// identity that will be served and, if it wins, adopted. An excluded or
	// failed substitute is refused here rather than served or persisted by the
	// caller.
	if virtualCandidateIDExcluded(candidate.URI, excludedCandidateIDs) {
		return nil, fmt.Errorf("%w: resolved candidate %q", errVirtualCandidateExcluded, virtualResultCandidateID(candidate.URI))
	}
	if err := h.virtualCandidateVerdictError(ctx, candidate.URI, file, ownerID, allowFailed); err != nil {
		return nil, err
	}
	transient := *file
	transient.FilePath = candidate.URI
	transient.VirtualOwnerInstallationID = ownerID
	// The stale row is a different concrete release, so its FileSize is not
	// this substitute's declaration. The probe input (and every evidence
	// decision below) carries the substitute's own listed size; the transient
	// is only a probe input, so replacing that field here cannot rewrite the
	// stale row's advertised size.
	probeInput := transient
	probeInput.FileSize = candidate.FileSize
	resolved := resolvedVirtualPlaybackSource{
		URL: streamURL, URI: candidate.URI, OwnerID: ownerID, File: &probeInput, Provenance: ProbeProvenanceDeclared,
		ResolvedURL: streamURL, ResolvedURLExpiresAt: resolvedExpiresAt,
		ProviderVideoHash: providerVideoHash, ProviderGUID: providerGUID,
		ProviderReleaseName: providerReleaseName, ProviderReleaseSize: providerReleaseSize,
		IdentityRematched: identityRematched,
		RequestHeaders:    cloneHeaderMap(candidate.RequestHeaders),
	}
	if h.VirtualPlaybackSourceProber == nil && h.VirtualPlaybackSourceProberWithHeaders == nil {
		return &resolved, nil
	}
	expectedRuntimeMinutes := h.virtualExpectedRuntimeMinutes(ctx, file)
	evidencePin := scanner.VirtualServedSizePin(candidate.URI, ownerID)
	// Probe-cache-first, same seam the shared resolve uses: a version-switch or
	// provider-churn candidate that already has a completed probe needs no
	// second synchronous ffprobe. The cache key strips the provider query, so a
	// renumbered ?result= or rotated credential still hits the same release's
	// cached bytes evidence.
	if cached, cachedObservation := h.virtualProbeFromCache(ctx, file, streamURL, probeInput, candidate, resolvedSourceIdentity(&resolved).tiers(), ownerID); cached != nil {
		resolved.File = cached
		resolved.ProbeSucceeded = true
		resolved.Provenance = ProbeProvenanceVerified
		// The hit is a completed probe, not necessarily the pin's identity: the
		// cached bytes can disagree with a served-size observation recorded since
		// they were measured, and virtualProbeFromCache serves that case without
		// writing. Take its verdict instead of classifying the bytes again. A
		// second classification is not a read-only check: it records the pin's
		// first observation, so the repeat pass would compare the hit against the
		// snapshot the first pass just wrote, read its own recording as the
		// confirming serve, and hand the caller bytes to adopt and stamp that
		// nothing confirmed. A mismatch never reaches this branch:
		// virtualProbeFromCache discards it.
		resolved.EvidenceObservation = cachedObservation
		if err := h.virtualCandidateVerdictError(ctx, candidate.URI, file, ownerID, allowFailed); err != nil {
			return nil, err
		}
		return &resolved, nil
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, virtualProbeBudget)
	forwardVirtualCandidateDeclaredAudio(&probeInput, candidate)
	probed, probeErr := h.probeVirtualSource(probeCtx, streamURL, &probeInput, candidate.RequestHeaders)
	probeCancel()
	if probeErr != nil || probed == nil {
		return nil, errors.New("virtual stream probe failed during fallback")
	}
	resolved.File = probed
	mergeVirtualCandidateTracks(resolved.File, candidate)
	resolved.ProbeSucceeded = true
	resolved.Provenance = ProbeProvenanceVerified
	// This path hands the probed inventory to the caller, which persists it with
	// StampProbe and adopts the substitute, so it must pass the same served-size
	// gate as every other acceptance point. The comparison baseline is the
	// substitute's own listed size: the stale row's FileSize describes the
	// release being left, and gating a cross-release substitution against it
	// would reject nearly every legitimate replacement.
	evidenceVerdict := classifyVirtualServedEvidenceV3(evidencePin, candidate.FileSize, &probeInput, probed, expectedRuntimeMinutes)
	if evidenceVerdict == scanner.VirtualServedEvidenceMismatch {
		// The probe found positive evidence that this is not the substitute's
		// release (duration or track layout). Refuse it here rather than let
		// the caller adopt it as the stale row's identity; the caller moves on
		// to the next candidate.
		virtualProbeFailures.markSizeMismatch(virtualProbeFailureKey(candidate.URI, ownerID), probed.ProbedFormatSize, candidate.FileSize)
		logVirtualProbeSizeMismatch(ctx, file.ID, candidate.URI, candidate.FileSize, probed.ProbedFormatSize)
		return nil, fmt.Errorf("candidate %s is not the declared release (probed %d bytes, declared %d)",
			candidate.URI, probed.ProbedFormatSize, candidate.FileSize)
	}
	if scanner.VirtualServedSizeMismatch(candidate.FileSize, probed.ProbedFormatSize) {
		logVirtualProbeSizeGapV3(ctx, file.ID, candidate.URI, candidate.FileSize, probed.ProbedFormatSize, probed.Duration, expectedRuntimeMinutes,
			virtualProbeEvidenceWithholdsIdentity(evidenceVerdict))
	}
	if virtualProbeEvidenceWithholdsIdentity(evidenceVerdict) {
		// The bytes play for this request and their inventory is written, but
		// they only recorded the pin's first served-size observation. The
		// caller must not stamp or pin them: that would make one unconfirmed
		// serve the pin's identity (see virtualProbeEvidenceWithholdsIdentity).
		resolved.EvidenceObservation = true
	}
	// Adoption fence. The probe can take seconds, during which the serve layer
	// (or another replan) may record a failed verdict for the identity we are
	// about to return and persist. The pre-resolve and post-resolve checks
	// cannot see that verdict, so re-check once more after the probe completes
	// and before the caller can adopt this source.
	if err := h.virtualCandidateVerdictError(ctx, candidate.URI, file, ownerID, allowFailed); err != nil {
		return nil, err
	}
	return &resolved, nil
}

func filterVirtualPlaybackStreams(file *models.MediaFile, streams []VirtualPlaybackStream) []VirtualPlaybackStream {
	if file == nil {
		return nil
	}
	base, err := url.Parse(file.FilePath)
	if err != nil {
		return nil
	}
	baseProfile := strings.TrimSpace(base.Query().Get("profile"))
	seen := map[string]struct{}{file.FilePath: {}}
	alternatives := make([]VirtualPlaybackStream, 0, len(streams))
	for _, stream := range streams {
		if len(alternatives) >= maxVirtualPlaybackStreams-1 {
			break
		}
		stream.URI = strings.TrimSpace(stream.URI)
		if !strings.HasPrefix(strings.ToLower(stream.URI), virtualPlaybackPrefix) {
			continue
		}
		candidate, parseErr := url.Parse(stream.URI)
		if parseErr != nil ||
			!strings.EqualFold(candidate.Scheme, base.Scheme) ||
			!strings.EqualFold(candidate.Host, base.Host) ||
			candidate.EscapedPath() != base.EscapedPath() {
			continue
		}
		if baseProfile != "" && !strings.EqualFold(
			strings.TrimSpace(candidate.Query().Get("profile")), baseProfile,
		) {
			continue
		}
		if _, duplicate := seen[stream.URI]; duplicate {
			continue
		}
		seen[stream.URI] = struct{}{}
		alternatives = append(alternatives, stream)
	}
	return alternatives
}

func visibleVirtualPlaybackStreams(streams []VirtualPlaybackStream) []VirtualPlaybackStream {
	visible := make([]VirtualPlaybackStream, 0, len(streams))
	hasHidden := hasHiddenVirtualPlaybackStreams(streams)
	for _, stream := range streams {
		if stream.Visible || !hasHidden {
			visible = append(visible, stream)
		}
	}
	return visible
}

func hasHiddenVirtualPlaybackStreams(streams []VirtualPlaybackStream) bool {
	for _, stream := range streams {
		if stream.VisibilitySpecified && !stream.Visible {
			return true
		}
	}
	return false
}

func cloneHeaderMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isVirtualPlaybackFile(file *models.MediaFile) bool {
	return file != nil && strings.HasPrefix(file.FilePath, virtualPlaybackPrefix)
}

func isUnplayableVirtualURI(uri string) bool {
	raw := strings.TrimSpace(strings.ToLower(uri))
	if strings.HasPrefix(raw, "virtual://series/") || strings.HasPrefix(raw, "virtual://show/") {
		parsed, err := url.Parse(raw)
		if err == nil {
			trimmed := strings.Trim(parsed.Path, "/")
			if trimmed == "" {
				return true
			}
			parts := strings.Split(trimmed, "/")
			if len(parts) < 3 {
				return true
			}
		}
	}
	return false
}

func withVirtualResultKey(virtualPath, candID string) string {
	if candID == "" {
		return virtualPath
	}
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return virtualPath + "?result=" + candID
	}
	q := parsed.Query()
	q.Set("result", candID)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// virtualPlaybackNeutralKey returns the virtual URI with any concrete "result="
// pick removed, preserving the scheme/host/path and the profile so a stale
// provider candidate can be re-resolved provider-neutrally within the same
// quality selection.
func virtualPlaybackNeutralKey(virtualPath string) string {
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return virtualPath
	}
	q := parsed.Query()
	if strings.TrimSpace(q.Get("result")) == "" {
		return virtualPath
	}
	q.Del("result")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// virtualResultCandidateID returns the concrete "result=" candidate ID bound
// to a virtual URI, or "" when the URI carries no explicit pick.
func virtualResultCandidateID(virtualPath string) string {
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("result"))
}

// virtualSubtitleSearchKey identifies one in-flight subtitle search. It binds
// the catalog row to the concrete candidate URI (or the resolved identity) so
// only identical requests dedupe; distinct release candidates for the same row
// each reach the provider. A row without a positive ID falls back to its
// content identity.
func virtualSubtitleSearchKey(file *models.MediaFile, cand VirtualPlaybackStream) string {
	if file != nil && file.ID > 0 {
		return "virtual-file:" + strconv.Itoa(file.ID) + ":" + cand.URI
	}
	contentID := ""
	if file != nil {
		contentID = file.ContentID
	}
	return "virtual:" + contentID + ":" + cand.URI
}

// maybeTriggerSubtitleSearch kicks off a background subtitle search when a
// virtual stream enters playback with no embedded or external subtitle tracks.
// Results are downloaded and associated with the file so they appear in the
// player's subtitle selector without blocking playback start.
//
// Admission takes two slots before the goroutine is spawned: one from the
// aggregate detached gate and one from the tighter subtitle-search gate. Both
// are held until the callback returns — never released merely because the
// search context expired — so a provider that ignores cancellation cannot have
// a replacement goroutine admitted behind it. Shutdown cancels the search
// context but does not touch the slots; the callback's deferred cleanup is the
// only place they are released, which keeps "capacity reserved until the
// callback exits" true even on the non-cooperative path.
func (h *PlaybackHandler) maybeTriggerSubtitleSearch(
	ctx context.Context,
	file *models.MediaFile,
	cand VirtualPlaybackStream,
) {
	if h.VirtualSubtitleSearcher == nil || file == nil {
		return
	}
	if len(file.SubtitleTracks) > 0 || len(file.ExternalSubtitles) > 0 {
		return
	}
	// The key binds the row to the concrete candidate URI, not just the file
	// ID: two concurrent replans of the same row can carry different release
	// candidates, and each is a distinct provider search (different bytes and
	// subtitle languages). Keying on the file ID alone silently dropped the
	// second legitimate search.
	searchKey := virtualSubtitleSearchKey(file, cand)
	// Admit before touching the dedupe map: a saturated budget must shed the
	// search without leaving a key behind.
	gate := h.detachedGate()
	if !gate.tryAcquire() {
		slog.WarnContext(ctx, "virtual subtitle search skipped: detached worker budget exhausted",
			"component", "api", "content_id", file.ContentID)
		return
	}
	slots := h.subtitleSearchGate()
	if !slots.tryAcquire() {
		gate.release()
		slog.WarnContext(ctx, "virtual subtitle search skipped: subtitle search budget exhausted",
			"component", "api", "content_id", file.ContentID, "cap", virtualSubtitleSearchCap)
		return
	}
	if h.SubtitleSearchInFlight == nil {
		h.SubtitleSearchInFlight = &sync.Map{}
	}
	// Dedupe: one in-flight search per (row, candidate). Rapid replays or
	// identical requests for the same candidate must not hammer subtitle
	// providers, but a distinct candidate for the same row is a distinct
	// search and must still reach the provider.
	if _, loaded := h.SubtitleSearchInFlight.LoadOrStore(searchKey, struct{}{}); loaded {
		slog.DebugContext(ctx, "virtual subtitle search skipped: identical request already in flight",
			"component", "api", "file_id", file.ID, "candidate_uri", cand.URI)
		slots.release()
		gate.release()
		return
	}
	go func() {
		// Release only after the callback has actually returned. A context
		// expiry inside the callback must not free the slot: the goroutine may
		// still be in a provider RPC, and admitting a replacement there is the
		// unbounded-replacement bug this ordering avoids.
		defer func() {
			h.SubtitleSearchInFlight.Delete(searchKey)
			slots.release()
			gate.release()
		}()
		// The search outlives the request but is bounded and follows the
		// service lifecycle, so a hung provider cannot leak the goroutine
		// forever and shutdown stops it. The context carries a finite deadline
		// through to the provider search/download and the persistence callback.
		searchCtx, searchCancel := h.virtualDetachedContext(ctx, virtualSubtitleSearchBudget)
		defer searchCancel()
		// Re-check after admission: a shutdown or budget that fired while the
		// goroutine was being scheduled must not start new provider I/O.
		if err := searchCtx.Err(); err != nil {
			slog.DebugContext(ctx, "virtual subtitle search not started: context already done",
				"component", "api", "content_id", file.ContentID, "error", err)
			return
		}
		h.VirtualSubtitleSearcher(
			searchCtx,
			file.ContentID,
			"", // IMDb ID resolved from contentID by the caller
			"", // title resolved by the caller
			0,  // year resolved by the caller
			0,  // season
			0,  // episode
			file.ID,
			cand.SubtitleLanguages,
		)
	}()
}

// allowDeferredProbe reports whether the virtual probe may be pushed to the
// background. It follows the caller's deferProbe flag exactly.
func allowDeferredProbe(deferProbe bool) bool {
	return deferProbe
}

// virtualDeferredProbeForUnstampedRow decides and builds the post-commit probe
// for a virtual row that has never been probed (no probe stamp), so a start
// that serves the row provisionally still hands the start path a worker to run
// after the transport commits. Without it a direct-play validated start could
// serve a codec-populated, unstamped row and arm no worker at all: the
// inventory reported pending would never resolve to verified or failed, and
// only the convergence watchdog would end it.
//
// The returned probe is the only signal that a worker will run: a nil return
// means no worker was admitted, so the caller must serve declared metadata with
// no pending lifecycle rather than promise a resolution nothing will deliver.
// Admission shares the declared fast path's probe-failure damper: when a recent
// failure already consumed this candidate's probe budget the worker is not
// admitted. The cache-only recovery still runs, because an inner probe may have
// completed after an outer timeout and landed its evidence.
//
// It returns nil when the row already carries a stamp, no prober is configured,
// or the resolved URL is empty (nothing to enumerate). Value copies are captured
// now so a later mutation of the request's file cannot change what is probed.
func (h *PlaybackHandler) virtualDeferredProbeForUnstampedRow(
	ctx context.Context,
	stickyKey string,
	file *models.MediaFile,
	streamURL string,
	cand VirtualPlaybackStream,
	oid int,
) *virtualDeferredProbeV3 {
	if h == nil || file == nil {
		return nil
	}
	if file.ProbeUpdatedAt != nil {
		return nil
	}
	if h.VirtualPlaybackSourceProber == nil && h.VirtualPlaybackSourceProberWithHeaders == nil {
		return nil
	}
	if strings.TrimSpace(streamURL) == "" {
		return nil
	}
	probeTransient := cloneVirtualProbeTransient(*file)
	// Zero the duration so the deferred probe measures the empirical duration
	// instead of inheriting the catalog value.
	probeTransient.Duration = 0
	if virtualProbeBackedOff(virtualProbeFailureKey(cand.URI, oid)) {
		// Damper armed: a recent failure already consumed the probe budget, so
		// no worker is admitted. An inner probe may still have completed and
		// landed in the cache, so recover it before declining; the caller
		// serves declared metadata rather than a pending state nothing will
		// resolve.
		//
		// No fresh resolver identity exists on this fast path: the row owns the
		// persisted candidate and the provider was never listed or resolved, so
		// the release decision rests on the row's verified ownership (the zero
		// tiers contribute nothing to the shared-tier check) rather than an
		// identity invented from the row's own tiers.
		h.recoverVirtualProbeFromCache(ctx, file, streamURL, probeTransient, cand, resolver.PersistedIdentityTiers{}, oid)
		return nil
	}
	return &virtualDeferredProbeV3{
		stickyKey:              stickyKey,
		file:                   file,
		streamURL:              streamURL,
		probeTransient:         &probeTransient,
		cand:                   cand,
		expectedRuntimeMinutes: h.virtualExpectedRuntimeMinutes(ctx, file),
		ownerID:                oid,
	}
}

// mergeVirtualCandidateTracks supplements probed virtual file tracks with
// metadata from the provider candidate. ffprobe may not always detect
// language tags, codecs, or dimensions on remote streams (especially HLS
// and DASH), so candidate metadata fills the gaps so virtual files appear
// as close to local files as possible.
//
// Observed evidence is never overwritten: every fill below fires only when
// the probed field is empty/zero. A probed SDR/8-bit video track keeps its
// range, depth, profile, and DV fields even when the release text advertises
// HDR or Dolby Vision; a probed codec, language set, or container keeps its
// values even when the candidate declares different ones. Declarations may
// add what the probe missed, never replace what it found.
func mergeVirtualCandidateTracks(probed *models.MediaFile, candidate VirtualPlaybackStream) {
	if probed == nil {
		return
	}

	// Fill empty top-level fields that ffprobe may miss on remote streams.
	// A trackless, resolution-less file keeps CodecVideo/CodecAudio/Container
	// empty so the evidence gates below report it incomplete: without a
	// resolution the planner cannot pick a route, and claiming synthesized
	// evidence would only hide the missing metadata that still blocks
	// planning. The caller-supplied baselines (declaredFallback fixes up
	// transient.Resolution first) flow through because a non-empty candidate
	// label lands in probed.Resolution on the line below.
	if probed.Resolution == "" {
		probed.Resolution = candidate.Resolution
	}
	// Top-level codec derivation defers to the track-evidence-first blocks
	// below (lines ~1883+, ~1903+): existing tracks win over candidate blobs,
	// and the final CodecVideo/CodecAudio assignment happens there exactly
	// once so merge stays idempotent.
	hasResolution := probed.Resolution != ""
	if probed.Container == "" || strings.EqualFold(probed.Container, "virtual") {
		if candidate.Container != "" && !strings.EqualFold(candidate.Container, "virtual") {
			probed.Container = candidate.Container
		} else if hasResolution {
			probed.Container = "mkv"
		}
	}
	if probed.FileSize == 0 {
		probed.FileSize = candidate.FileSize
	}
	if probed.Bitrate == 0 {
		probed.Bitrate = candidate.Bitrate
	}
	if probed.Bitrate == 0 && probed.FileSize > 0 && probed.Duration > 0 {
		probed.Bitrate = int((probed.FileSize * 8) / int64(probed.Duration) / 1000)
	}
	// Only a known resolution justifies a synthesized bitrate; the fallback
	// table is resolution-keyed. Leaving it unset for an unknown resolution
	// keeps the planner's source_metadata_incomplete detail honest ("missing:
	// bitrate") instead of reporting a fabricated 10000 kbps and masking the
	// gap. mergeVirtualCandidateTracks must not manufacture evidence the probe
	// did not provide.
	if probed.Bitrate == 0 && hasResolution {
		probed.Bitrate = virtualBitrateFallback(probed.Resolution)
	}

	// Codec precedence: existing top-level wins; if empty, use first track
	// codec; then candidate; then resolution-gated default. The top-level
	// scalar is preserved when non-empty because it may reflect a probed
	// value that is more authoritative than the first track in the slice.
	if probed.CodecAudio == "" && len(probed.AudioTracks) > 0 && probed.AudioTracks[0].Codec != "" {
		probed.CodecAudio = probed.AudioTracks[0].Codec
	}
	if probed.CodecAudio == "" {
		probed.CodecAudio = candidate.CodecAudio
	}
	if probed.CodecAudio == "" && hasResolution {
		probed.CodecAudio = "aac"
	}
	channels := inferChannelsFromCodec(probed.CodecAudio)
	if channels <= 0 {
		channels = 2
	}
	if probed.AudioChannels <= 0 {
		probed.AudioChannels = channels
	}

	// Create a basic video track when ffprobe didn't detect any. Codec
	// precedence: existing top-level > first track > candidate > h264 default
	// (only when resolution is present, to avoid false claims on
	// resolution-less incomplete metadata).
	videoCodec := probed.CodecVideo
	if videoCodec == "" && len(probed.VideoTracks) > 0 && probed.VideoTracks[0].Codec != "" {
		videoCodec = probed.VideoTracks[0].Codec
	}
	if videoCodec == "" {
		videoCodec = candidate.CodecVideo
	}
	if videoCodec == "" && probed.Resolution != "" {
		videoCodec = "h264"
	}
	if probed.CodecVideo == "" && videoCodec != "" {
		probed.CodecVideo = videoCodec
	}
	isDV, dvProfile := virtualDVMetadata(candidate.HDR)
	// A declared Dolby Vision label cannot apply to a measured codec that cannot
	// carry a DV RPU (see playback.CodecCarriesDolbyVisionV3): an "H.264 DV"
	// release is a mislabel. Trust the codec the probe measured and drop only the
	// impossible part of the declaration, so an SDR file is not persisted as
	// Dolby Vision and planned as a tone-mapped HDR transcode. A label that also
	// names HDR10/HLG keeps that part; a DV-only label leaves no HDR claim.
	declaredHDR := candidate.HDR
	if isDV && videoCodec != "" && !playback.CodecCarriesDolbyVisionV3(videoCodec) {
		isDV, dvProfile = false, 0
		lower := strings.ToLower(declaredHDR)
		if !strings.Contains(lower, "hdr10") && !strings.Contains(lower, "hlg") {
			declaredHDR = ""
		}
	}
	isHDR := probed.HDR || declaredHDR != ""
	// The top-level HDR flag is observed evidence, not a declaration pad: it
	// flips from the candidate's label only when the declaration describes
	// tracks the merge will actually repaint. Flipping it while every probed
	// track keeps SDR would leave contradictory metadata (flag HDR, tracks
	// SDR) for the planner to trip over.
	if !probed.HDR && virtualCandidateDeclaresHDRForObservedTracks(probed, isHDR, isDV) {
		probed.HDR = true
		isHDR = true
	}
	defaultProfile, defaultLevel, defaultBitDepth := defaultVirtualVideoProfileAndLevel(videoCodec, isHDR, isDV, probed.Resolution)
	if len(probed.VideoTracks) == 0 && probed.Resolution != "" {
		videoRange := videoRangeSDR
		videoRangeType := videoRangeSDR
		if isDV {
			if dvProfile == 0 {
				dvProfile = 8
			}
			videoRange = "DolbyVision"
			videoRangeType = videoRangeDOVI
		} else if isHDR {
			videoRange = "HDR"
			videoRangeType = videoRangeHDR10
		}
		vt := models.VideoTrack{
			Codec:          videoCodec,
			Profile:        defaultProfile,
			Level:          defaultLevel,
			Width:          resolutionWidth(probed.Resolution),
			Height:         resolutionHeight(probed.Resolution),
			FrameRate:      defaultVirtualFrameRate(candidate.FrameRate),
			BitDepth:       defaultBitDepth,
			Bitrate:        probed.Bitrate,
			VideoRange:     videoRange,
			VideoRangeType: videoRangeType,
			DVProfile:      dvProfile,
			DolbyVision:    virtualDVLabel(isDV, dvProfile),
		}
		if isDV && dvProfile != 5 {
			vt.DVConfigPresent = true
			vt.DVBLCompatIDPresent = true
			vt.DVBLCompatID = 1
			vt.DVBLPresent = true
		}
		probed.VideoTracks = append(probed.VideoTracks, vt)
	}
	for i := range probed.VideoTracks {
		if probed.VideoTracks[i].Codec == "" {
			probed.VideoTracks[i].Codec = videoCodec
		}
		// Eligibility is frozen from the probe's original track, before this
		// pass fills anything: declaration-derived fills below (depth,
		// DVProfile, range strings) must not flip the verdict mid-track, or
		// a bare SDR track acquires DV fields while keeping SDR (and vice
		// versa, an empty-range track gains depth and then keeps the depth
		// from blocking its own range assignment).
		repaintable := virtualTrackRangeRepaintable(probed.VideoTracks[i])
		if !repaintable {
			// The probe corroborated this track's range (per-track
			// evaluation — never the first track's): keep it. The candidate
			// may still fill dimensional gaps (width/height/frame-rate) via
			// the `<= 0`/`== ""` guards below, which never overwrite — but no
			// profile, level, DV field, or range string is recomputed from
			// the candidate's HDR/DV labels. A bare range string with no
			// supporting evidence stays repaintable (the stale-SDR repair
			// below), so only corroborated ranges take this branch. Flipping
			// a probed range here is what persisted misinformation that drove
			// wrong direct-play, DV, and tone-map routes.
			//
			// Bit depth is the exception to the no-recompute rule: an
			// observed range TYPE (HDR10/HLG/DOVI) is itself subtype evidence
			// that the stream carries ≥10-bit samples (BT.2020/ST 2084
			// pipelines cannot be 8-bit), so a corroborated HDR-type track
			// with unknown depth (0) is filled from the codec/range default
			// rather than left at 0 to terminal source_metadata_incomplete.
			// An observed 8-bit depth is still kept: depth is observed
			// evidence even with an empty range string, and only the
			// explicit HDR/DV/profile upgrade below may lift it.
			if probed.VideoTracks[i].Width <= 0 {
				probed.VideoTracks[i].Width = resolutionWidth(probed.Resolution)
			}
			if probed.VideoTracks[i].Height <= 0 {
				probed.VideoTracks[i].Height = resolutionHeight(probed.Resolution)
			}
			if probed.VideoTracks[i].FrameRate == "" {
				probed.VideoTracks[i].FrameRate = defaultVirtualFrameRate(candidate.FrameRate)
			}
			if probed.VideoTracks[i].BitDepth <= 0 {
				// Derive HDR/DV status from the track's own observed
				// evidence, not the outer candidate flags: a corroborated
				// HDR-type track on an SDR-labeled release (no HDR text in
				// the release name, top-level flag false) must still resolve
				// 10-bit. The outer flags alone would return depth 8 for a
				// 1080p hevc track and violate the BT.2020/ST 2084 floor.
				track := probed.VideoTracks[i]
				trackIsDV := track.DVProfile > 0 || track.DolbyVision != "" || track.DVConfigPresent || track.DVBLPresent ||
					strings.HasPrefix(strings.ToLower(track.VideoRangeType), "dovi")
				trackIsHDR := trackIsDV || track.HDR10Plus || strings.EqualFold(track.VideoRange, "hdr") ||
					(!strings.EqualFold(track.VideoRangeType, "sdr") && strings.TrimSpace(track.VideoRangeType) != "")
				if _, _, trackDepth := defaultVirtualVideoProfileAndLevel(track.Codec, isHDR || trackIsHDR, isDV || trackIsDV, probed.Resolution); trackDepth > 0 {
					probed.VideoTracks[i].BitDepth = trackDepth
				}
			}
			continue
		}
		trackProf, trackLvl, trackDepth := defaultVirtualVideoProfileAndLevel(probed.VideoTracks[i].Codec, isHDR, isDV, probed.Resolution)
		if probed.VideoTracks[i].Profile == "" {
			probed.VideoTracks[i].Profile = trackProf
		}
		if probed.VideoTracks[i].Level <= 0 {
			probed.VideoTracks[i].Level = trackLvl
		}
		if probed.VideoTracks[i].Width <= 0 {
			probed.VideoTracks[i].Width = resolutionWidth(probed.Resolution)
		}
		if probed.VideoTracks[i].Height <= 0 {
			probed.VideoTracks[i].Height = resolutionHeight(probed.Resolution)
		}
		if probed.VideoTracks[i].FrameRate == "" {
			probed.VideoTracks[i].FrameRate = defaultVirtualFrameRate(candidate.FrameRate)
		}
		if probed.VideoTracks[i].BitDepth <= 0 {
			probed.VideoTracks[i].BitDepth = trackDepth
		} else if probed.VideoTracks[i].BitDepth == 8 && (isHDR || isDV || strings.EqualFold(probed.VideoTracks[i].Profile, "main 10")) {
			probed.VideoTracks[i].BitDepth = 10
		}
		if probed.VideoTracks[i].Bitrate <= 0 {
			probed.VideoTracks[i].Bitrate = probed.Bitrate
		}
		if isDV {
			profile := probed.VideoTracks[i].DVProfile
			if profile == 0 {
				profile = dvProfile
				if profile == 0 {
					profile = 8
				}
				probed.VideoTracks[i].DVProfile = profile
			}
			if probed.VideoTracks[i].DolbyVision == "" {
				probed.VideoTracks[i].DolbyVision = virtualDVLabel(true, profile)
			}
			if repaintable &&
				(strings.TrimSpace(probed.VideoTracks[i].VideoRange) == "" ||
					strings.EqualFold(strings.TrimSpace(probed.VideoTracks[i].VideoRange), "sdr")) {
				probed.VideoTracks[i].VideoRange = "DolbyVision"
				probed.VideoTracks[i].VideoRangeType = videoRangeDOVI
			}
			if profile != 5 && !probed.VideoTracks[i].DVConfigPresent {
				probed.VideoTracks[i].DVConfigPresent = true
				probed.VideoTracks[i].DVBLCompatIDPresent = true
				probed.VideoTracks[i].DVBLCompatID = 1
				probed.VideoTracks[i].DVBLPresent = true
			}
		} else if isHDR && repaintable &&
			(strings.TrimSpace(probed.VideoTracks[i].VideoRange) == "" ||
				strings.EqualFold(strings.TrimSpace(probed.VideoTracks[i].VideoRange), "sdr")) {
			probed.VideoTracks[i].VideoRange = "HDR"
			probed.VideoTracks[i].VideoRangeType = videoRangeHDR10
		} else if isHDR && repaintable && probed.VideoTracks[i].VideoRange == videoRangeSDR && probed.VideoTracks[i].VideoRangeType == "" {
			// A stale SDR range string the probe left without a type (the
			// pre-type era wrote range-only rows): repaint it. A corroborated
			// range never reaches here because repaintable was frozen false
			// above. (Subsumed by the sdr branch when the type mirror has
			// already copied range→type; kept explicit for the exact stale
			// shape.)
			probed.VideoTracks[i].VideoRange = "HDR"
			probed.VideoTracks[i].VideoRangeType = videoRangeHDR10
		}
		if probed.VideoTracks[i].VideoRange == "" && !probed.HDR {
			probed.VideoTracks[i].VideoRange = videoRangeSDR
			probed.VideoTracks[i].VideoRangeType = videoRangeSDR
		}
		if probed.VideoTracks[i].VideoRangeType == "" && probed.VideoTracks[i].VideoRange != "" {
			probed.VideoTracks[i].VideoRangeType = probed.VideoTracks[i].VideoRange
		}
	}

	// Synthesize audio tracks from the provider-declared languages when the
	// probe left the inventory empty. A file with no resolution and no tracks
	// cannot produce a routable plan, so language synthesis is gated on the
	// caller supplying a resolution (declared, backfilled, or probed) — via
	// the baseline or a real candidate value. Video track synthesis below
	// follows the same gate so both inventories stay consistent.
	mergeVirtualCandidateLanguages(probed, candidate)

	if len(probed.AudioTracks) == 0 && probed.Resolution != "" {
		probed.AudioTracks = []models.AudioTrack{{
			Codec:    probed.CodecAudio,
			Channels: channels,
			Default:  true,
		}}
	}

	// Fill audio channels and codec on existing tracks that lack them.
	for i := range probed.AudioTracks {
		if probed.AudioTracks[i].Codec == "" {
			probed.AudioTracks[i].Codec = probed.CodecAudio
		}
		if probed.AudioTracks[i].Channels == 0 {
			probed.AudioTracks[i].Channels = channels
		}
	}

	if len(probed.AudioTracks) > 0 {
		hasDefault := false
		for _, t := range probed.AudioTracks {
			if t.Default {
				hasDefault = true
				break
			}
		}
		if !hasDefault {
			probed.AudioTracks[0].Default = true
		}
	}
}

// forwardVirtualCandidateDeclaredAudio copies a served candidate's declared
// audio evidence onto the file handed to the prober, so
// scanner.ProbeVirtualSource can tell a partial enumeration from a genuinely
// video-only source.
//
// The probe input on the background/rotation and fallback paths is the catalog
// row alone (or a thin probe transient), which can carry no audio declaration
// even when the candidate the session resolved does — a provider that reports
// audio languages without a codec, or a fresh alternate-version row whose
// synthetic tracks have not landed. With no expected-audio signal,
// scanner.virtualSourceExpectsAudio reports false and the prober accepts a
// video-with-subtitles-but-no-audio result as complete; the session then
// publishes that partial menu as verified. Forwarding the declaration closes
// that path.
//
// It forwards only what the candidate carries and never infers audio from a
// resolution, so a video-only candidate that declares no audio stays a valid,
// complete enumeration. The synthesized track carries no container index, and
// applyProbeData replaces the whole audio slice on a complete probe, so the
// forwarded declaration never survives into persisted probe evidence.
func forwardVirtualCandidateDeclaredAudio(file *models.MediaFile, cand VirtualPlaybackStream) {
	if file == nil {
		return
	}
	declaredCodec := strings.TrimSpace(cand.CodecAudio)
	if strings.TrimSpace(file.CodecAudio) == "" && declaredCodec != "" {
		file.CodecAudio = declaredCodec
	}
	if len(file.AudioTracks) > 0 {
		return
	}
	for _, lang := range cand.AudioLanguages {
		lang = strings.TrimSpace(lang)
		if lang == "" || !isRealVirtualLanguageTag(lang) {
			continue
		}
		file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Language: lang, Codec: declaredCodec})
	}
}

// virtualCandidateDeclaresHDRForObservedTracks reports whether the
// candidate's HDR/DV labels describe tracks the merge will actually repaint.
// The top-level HDR flag follows the tracks: when every probed track keeps
// its observed SDR range (none repaintable), the flag must not flip from the
// declaration alone, or the file carries contradictory metadata (flag HDR,
// tracks SDR). With no probed tracks (synthesis path) or at least one
// repaintable track, the declaration applies as before. Callers compute
// isHDR/isDV first and pass them in so the flag decision sees the same
// declaration parse the repaint path uses.
func virtualCandidateDeclaresHDRForObservedTracks(probed *models.MediaFile, isHDR, isDV bool) bool {
	if !isHDR && !isDV {
		return false
	}
	if len(probed.VideoTracks) == 0 {
		return true
	}
	for _, track := range probed.VideoTracks {
		if virtualTrackRangeRepaintable(track) {
			return true
		}
	}
	return false
}

// virtualTrackRangeRepaintable reports whether a probed track's range may be
// repainted from the candidate's HDR/DV labels. Only a range the probe never
// corroborated is repaintable: an empty range pair, or a bare "SDR" range
// with no other observed range evidence on that same track (no depth, no
// HDR-range profile, no DV fields, no HDR10Plus, no non-SDR range type). A
// stale SDR string left by an older probe with no supporting evidence is
// repairable; an SDR string the probe corroborated (8-bit depth alone counts
// — depth is observed evidence even with an empty range string) is observed
// and kept, as is any track carrying a non-SDR range type (HLG, DOVI,
// HDR10): the type is observed subtype evidence even when the range string
// itself is empty. Evaluation is per track: one track's corroboration never
// protects (or exposes) another.
func virtualTrackRangeRepaintable(track models.VideoTrack) bool {
	if track.BitDepth != 0 || track.DVProfile != 0 || track.DolbyVision != "" ||
		track.DVConfigPresent || track.DVBLPresent || track.HDR10Plus {
		return false
	}
	if rangeType := strings.TrimSpace(track.VideoRangeType); rangeType != "" &&
		!strings.EqualFold(rangeType, "sdr") {
		return false
	}
	if strings.TrimSpace(track.VideoRange) == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(track.VideoRange), "sdr")
}

func defaultVirtualVideoProfileAndLevel(codec string, isHDR, isDV bool, res string) (string, int, int) {
	c := strings.ToLower(strings.TrimSpace(codec))
	switch c {
	case "hevc", "h265", "hev1", "hvc1":
		if isHDR || isDV || strings.EqualFold(res, "2160p") || strings.EqualFold(res, "4k") {
			return "main 10", 153, 10
		}
		return "main", 120, 8
	case "h264", "avc", "avc1":
		return "high", 41, 8
	case "av1", "av01":
		if isHDR || isDV {
			return "main", 153, 10
		}
		return "main", 153, 8
	case "vp9":
		if isHDR || isDV {
			return "profile 2", 41, 10
		}
		return "profile 0", 41, 8
	default:
		if isHDR || isDV {
			return "main 10", 153, 10
		}
		return "high", 41, 8
	}

}

var virtualDVProfileRegex = regexp.MustCompile(`(?i)(?:profile|dovi|dv|dolby\s*vision)\s*[-._:]?\s*0*([1-9]|1\d|20)(?:[.]\d+)?(?:[^a-z0-9]|$)`)

func virtualDVMetadata(raw string) (bool, int) {
	lower := strings.ToLower(strings.TrimSpace(raw))
	isDV := strings.Contains(lower, "dolby vision") || strings.Contains(lower, "dovi") ||
		lower == "dv" || strings.HasPrefix(lower, "dv ") || strings.HasSuffix(lower, " dv") || strings.Contains(lower, " dv ") ||
		virtualDVProfileMarker(lower)
	if !isDV {
		return false, 0
	}
	if matches := virtualDVProfileRegex.FindStringSubmatch(lower); len(matches) > 1 {
		if profile, err := strconv.Atoi(matches[1]); err == nil && profile > 0 && profile <= 20 {
			return true, profile
		}
	}
	return true, 0
}

func virtualDVProfileMarker(raw string) bool {
	for _, profile := range []string{"profile 5", "profile 7", "profile 8", "dv5", "dv7", "dv8"} {
		if strings.Contains(raw, profile) {
			return true
		}
	}
	return false
}

func mediaFileHDRString(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	if dvProfile := file.PrimaryDVProfile(); dvProfile > 0 {
		return fmt.Sprintf("Dolby Vision Profile %d", dvProfile)
	}
	if len(file.VideoTracks) > 0 {
		vt := file.VideoTracks[0]
		if vt.DolbyVision != "" {
			return vt.DolbyVision
		}
		if strings.EqualFold(vt.VideoRange, "DolbyVision") || strings.EqualFold(vt.VideoRangeType, videoRangeDOVI) {
			return "Dolby Vision"
		}
		if vt.VideoRange != "" && !strings.EqualFold(vt.VideoRange, videoRangeSDR) {
			return vt.VideoRange
		}
	}
	if file.HDR {
		return "true"
	}
	return ""
}

func virtualDVLabel(isDV bool, profile int) string {
	if !isDV || profile <= 0 {
		return ""
	}
	return "Profile " + strconv.Itoa(profile)
}

// declaredVirtualAudioTracks builds an audio inventory from a candidate's
// provider-declared languages. It is a placeholder, not probe evidence: the
// tags come from release metadata (e.g. a MULTi token in a filename), may be
// invented, and carry no real container stream index. It reuses
// mergeVirtualCandidateLanguages so the same tag filtering and deduplication
// apply; an unrecognized or empty declaration yields no tracks rather than an
// invented label. fallbackCodec carries the row's codec when the candidate
// declares none, so a replacement row does not lose the audio codec.
//
// Subtitles are deliberately not synthesized: a synthesized SubtitleTrack
// carries an ordinal no real stream backs, and the extractor maps it straight
// to ffmpeg's 0:s:N, which is the phantom-stream failure this module already
// documents. Declared subtitle languages never become embedded tracks; the
// probe and the subtitle search own that inventory.
func declaredVirtualAudioTracks(codecAudio string, languages []string, fallbackCodec string) []models.AudioTrack {
	if len(languages) == 0 {
		return nil
	}
	if codecAudio == "" {
		codecAudio = fallbackCodec
	}
	probed := &models.MediaFile{}
	mergeVirtualCandidateLanguages(probed, VirtualPlaybackStream{
		CodecAudio:     codecAudio,
		AudioLanguages: languages,
	})
	return probed.AudioTracks
}

// mergeVirtualCandidateLanguages appends provider-declared audio languages as
// tracks when the probed inventory does not already carry them. Candidate
// language lists come from the release metadata (e.g. ITA-ENG in a release
// name), not from ffprobe, so tracks synthesized here are never authoritative —
// a later probe fills real codec/channel evidence on top. Release-group markers
// that are not real languages (e.g. MULTI, DUAL) are skipped so a bogus track
// never appears in the player's picker.
//
// Provider-declared subtitle languages are deliberately NOT synthesized into
// embedded tracks here: a synthesized SubtitleTrack carries a stream ordinal
// that no real subtitle stream backs, and the extractor maps it straight to
// ffmpeg's `0:s:N` specifier. That produced phantom subtitle selections that
// always failed at FFmpeg ("Stream map ” matches no streams"). Real embedded
// subtitle inventory comes only from the probe; provider subtitle hints stay on
// the candidate stream for the picker and drive the background subtitle search.
func mergeVirtualCandidateLanguages(probed *models.MediaFile, candidate VirtualPlaybackStream) {
	if probed == nil || len(probed.AudioTracks) > 0 {
		return
	}
	audioCodec := probed.CodecAudio
	if audioCodec == "" {
		audioCodec = candidate.CodecAudio
	}
	if audioCodec == "" {
		audioCodec = "aac"
	}
	channels := inferChannelsFromCodec(audioCodec)
	if len(candidate.AudioLanguages) > 0 {
		existing := make(map[string]bool, len(candidate.AudioLanguages))
		for _, candidateLang := range candidate.AudioLanguages {
			candidateLang = strings.TrimSpace(candidateLang)
			if candidateLang == "" || !isRealVirtualLanguageTag(candidateLang) {
				continue
			}
			canonical := langpkg.CanonicalTag(candidateLang)
			if canonical == "" {
				canonical = virtualLanguageBaseSubtag(candidateLang)
			}
			if existing[canonical] {
				continue
			}
			existing[canonical] = true
			probed.AudioTracks = append(probed.AudioTracks, models.AudioTrack{
				// Synthesized tracks carry no real container stream index; the
				// array position is the ordinal (audioStreamOrdinalV3 falls back
				// to it when Index <= 0).
				Language: candidateLang,
				Codec:    audioCodec,
				Channels: channels,
			})
		}
	}
}

// virtualLanguageBaseSubtag canonicalizes a language token to its ISO base
// subtag ("ITA" → "it", "en-US" → "en"), so probe-recorded codes and
// provider-declared codes dedup against the same key. Unparseable tokens fall
// back to the lowercased trimmed input.
func virtualLanguageBaseSubtag(value string) string {
	trimmed := strings.TrimSpace(value)
	if tag, err := language.Parse(trimmed); err == nil {
		if base, conf := tag.Base(); conf != language.No && base.String() != "" {
			return base.String()
		}
	}
	return strings.ToLower(trimmed)
}

// isRealVirtualLanguageTag reports whether a provider-declared language token
// parses as a real ISO language subtag. The naive lowercased canonical form
// cannot tell "multi" from a valid three-letter code like "fil", so parsing
// with the Unicode language tagger and requiring a concrete base subtag keeps
// release markers like MULTI/DUAL out of the synthesized track inventory.
func isRealVirtualLanguageTag(value string) bool {
	tag, err := language.Parse(value)
	if err != nil {
		return false
	}
	base, conf := tag.Base()
	return conf != language.No && base.String() != ""
}

func defaultVirtualFrameRate(rate string) string {
	if strings.TrimSpace(rate) == "" {
		return "24"
	}
	return rate
}

func virtualBitrateFallback(resolution string) int {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "2160p", "4k", "uhd":
		return 24000
	case "720p":
		return 5000
	case "480p":
		return 2500
	default:
		return 10000
	}
}

// inferChannelsFromCodec returns a plausible channel count for a codec string.
func inferChannelsFromCodec(codec string) int {
	switch strings.ToLower(codec) {
	case "atmos":
		return 8
	case "truehd", "dts-hd", "dts", "eac3", "ac3":
		return 6
	default:
		return 2
	}
}

// resolutionWidth returns a typical width for a resolution label.
func resolutionWidth(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "4320p", "8k":
		return 7680
	case "2160p", "4k", "uhd":
		return 3840
	case "1080p":
		return 1920
	case "720p":
		return 1280
	case "480p":
		return 720
	default:
		if left, right, ok := strings.Cut(strings.TrimSpace(label), "x"); ok {
			if w, err := strconv.Atoi(strings.TrimSpace(left)); err == nil && w > 0 && w <= 32768 {
				if h, err := strconv.Atoi(strings.TrimSpace(right)); err == nil && h > 0 && h <= 32768 {
					return w
				}
			}
		}
		return 0
	}
}

// resolutionHeight returns a typical height for a resolution label.
func resolutionHeight(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "4320p", "8k":
		return 4320
	case "2160p", "4k", "uhd":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "480p":
		return 480
	default:
		if left, right, ok := strings.Cut(strings.TrimSpace(label), "x"); ok {
			if w, err := strconv.Atoi(strings.TrimSpace(left)); err == nil && w > 0 && w <= 32768 {
				if h, err := strconv.Atoi(strings.TrimSpace(right)); err == nil && h > 0 && h <= 32768 {
					return h
				}
			}
		}
		return 0
	}
}

// qualityRungHeightV3 maps a normalized quality preference to its resolution
// class height. Only explicit fixed rungs return a nonzero height; "auto" and
// "original" return 0 so the caller keeps the device ranking unchanged.
// Compound ladder rungs ("1080p-high") carry their resolution class in the
// label prefix; compound reports whether the preference was one of those.
//
// The distinction matters for the bandwidth cap: ResolveQualityPolicyV3 lowers
// a plain fixed rung's class when its ladder bitrate exceeds the cap, but
// compoundRungQualityResultV3 never changes a compound rung's class (only its
// bitrate), so the picker must not lower a compound rung either.
func qualityRungHeightV3(qualityPreference string) (height int, compound bool) {
	normalized, _ := playback.NormalizeQualityV3(qualityPreference)
	class := normalized
	if idx := strings.IndexByte(class, '-'); idx > 0 {
		class = class[:idx]
		compound = true
	}
	switch class {
	case "2160p":
		height = 2160
	case "1080p":
		height = 1080
	case "720p":
		height = 720
	case "480p":
		height = 480
	case "420p":
		height = 420
	case "328p":
		height = 328
	}
	return height, compound && height > 0
}

// reorderVirtualCandidatesForQuality prefers candidates whose resolution class
// is at or below the requested fixed rung, keeping the device ranking stable
// within each group. When no candidate matches the rung (a provider that only
// offers higher resolutions), the device ranking is returned unchanged. The
// reorder is a preference, never a hard filter: a client that asked for 720p
// still gets the best device-ranked stream when no native 720p-or-below
// candidate exists.
//
// For a plain fixed rung the cap is applied per candidate through the planner's
// own playback.CappedRungHeightV3, using the candidate as the effective source.
// That keeps the picker and the planner in agreement: an explicit preference is
// reduced to the cap's rung only when the candidate's bitrate exceeds the cap,
// so a source-preserving encode under the cap is not displaced by a lower rung.
// A compound rung never changes class under a cap, so it keeps its class and the
// cap only clamps the planner's bitrate. "auto"/"original" return no rung and
// keep the device ranking untouched.
func reorderVirtualCandidatesForQuality(candidates []VirtualPlaybackStream, qualityPreference string, bandwidthCapKbps int) []VirtualPlaybackStream {
	rungHeight, compoundRung := qualityRungHeightV3(qualityPreference)
	if rungHeight <= 0 || len(candidates) <= 1 {
		return candidates
	}
	preferred := make([]VirtualPlaybackStream, 0, len(candidates))
	rest := make([]VirtualPlaybackStream, 0, len(candidates))
	for _, cand := range candidates {
		height := resolutionHeight(cand.Resolution)
		effectiveRung := rungHeight
		if height > 0 && !compoundRung {
			effectiveRung, _ = playback.CappedRungHeightV3(rungHeight, height, cand.Bitrate, bandwidthCapKbps)
		}
		if height > 0 && height <= effectiveRung {
			preferred = append(preferred, cand)
		} else {
			rest = append(rest, cand)
		}
	}
	if len(preferred) == 0 {
		return candidates
	}
	return append(preferred, rest...)
}

// canSkipProbeForContainer returns true for container formats that ffmpeg
// handles natively without needing ffprobe metadata. When a candidate
// already declares codecs, we skip the probe for these formats.
// ffprobe may return compound formats like "matroska,webm" or capitalized
// variants like "Matroska"; we check whether any recognized token appears.
func canSkipProbeForContainer(container string) bool {
	lowered := strings.ToLower(strings.TrimSpace(container))
	if lowered == "" {
		return false
	}
	known := []string{"mp4", "mkv", "webm", "ts", "m2ts", "mov", "avi", "flv", "wmv", "m4v", "mpeg", "mpg", "ogv", "3gp", "matroska"}
	for _, k := range known {
		if lowered == k || strings.Contains(lowered, k) {
			return true
		}
	}
	return false
}

// virtualMergedSupportsValidatedRoute reports whether merged candidate
// metadata can support a validated planner route, mirroring the deferred
// path's own preconditions: audio-only rows need audio evidence only, while
// video rows need planner-grade video evidence plus audio and container
// evidence. It is the deferral guard's predicate: defer the probe past
// commit only when the merged transient is route-plannable, otherwise run
// the bounded synchronous probe before the planner sees the row.
func virtualMergedSupportsValidatedRoute(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if file.IsAudioOnly() {
		return completeVirtualAudioEvidenceV3(file)
	}
	return completeVirtualVideoEvidenceV3(file) && completeVirtualAudioEvidenceV3(file) && completeVirtualContainerEvidenceV3(file)
}

// completeVirtualVideoEvidenceV3 reports whether a virtual file carries the
// detailed ffprobe video evidence the v3 planner needs to validate direct-play
// and stream-copy remux routes (profile/level/bit-depth/dimensions/frame rate/
// bitrate), as opposed to bare candidate codec declarations.
//
// It is deliberately the same predicate as the planner's
// playback.routeVideoMetadataCompleteV3 (codec, bit depth, dimensions, a frame
// rate that parses above zero, and a positive bitrate). Keeping the two in lock
// step is what stops a fast path from trusting a row the planner will reject as
// source_metadata_incomplete.
func completeVirtualVideoEvidenceV3(file *models.MediaFile) bool {
	return playback.VirtualRouteVideoMetadataCompleteV3(file)
}

// virtualFastPathPlannerVideoCompleteV3 reports whether a virtual row may take
// one of the stored-URL fast paths without stranding the planner on missing
// metadata. Video rows must carry the planner-grade video evidence; audio-only
// rows have no video route to validate and pass.
func virtualFastPathPlannerVideoCompleteV3(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	return file.IsAudioOnly() || completeVirtualVideoEvidenceV3(file)
}

// logVirtualFastPathSkippedV3 records that a previously-trusted stored-URL fast
// path was refused because the row lacks planner-grade video metadata. The
// missing-field wording matches the planner's source_metadata_incomplete detail
// so an operator can correlate the skip with the terminal it prevents.
func logVirtualFastPathSkippedV3(ctx context.Context, fastPath string, file *models.MediaFile, candidateURI string) {
	if file == nil {
		return
	}
	slog.InfoContext(ctx, "virtual fast path skipped for incomplete planner video metadata",
		"component", "api",
		"status", "fast_path_skipped_incomplete_metadata",
		"fast_path", fastPath,
		"file_id", file.ID,
		"content_id", file.ContentID,
		"candidate_uri", candidateURI,
		"missing", playback.VirtualRouteVideoMetadataGapsV3(file))
}

// completeVirtualAudioEvidenceV3 reports whether a virtual file carries the
// probed audio track evidence (codecs, channels, and layouts) required for
// the planner to validate direct play and surround sound passthrough.
func completeVirtualAudioEvidenceV3(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if file.IsAudioOnly() {
		return len(file.AudioTracks) > 0 && file.CodecAudio != ""
	}
	return len(file.AudioTracks) > 0 && file.AudioTracks[0].Codec != "" && file.AudioTracks[0].Channels > 0
}

// completeVirtualContainerEvidenceV3 reports whether the file's real container
// is known and usable for a server-mediated route. The canonical virtual row
// keeps Container="virtual" until a probe fills it in; once a prior play
// persisted the real container (mkv/webm/mp4/...), re-probing the same stream
// on every cold start adds nothing but a provider round-trip.
func completeVirtualContainerEvidenceV3(file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	return canSkipProbeForContainer(file.Container)
}

// marshalTracksJSON safely marshals track slices to JSON bytes for DB storage.
func marshalTracksJSON(tracks any) []byte {
	if tracks == nil {
		return []byte("[]")
	}
	data, err := json.Marshal(tracks)
	if err != nil {
		return []byte("[]")
	}
	return data
}

// sanitizeTrackSlice ensures the value is a slice/array, not a scalar.
// PostgreSQL jsonb array operations (like in triggers) fail with
// "cannot extract elements from a scalar" when given non-array jsonb.
func sanitizeTrackSlice(v any) any {
	if v == nil {
		return []any{}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return v
	}
	// Wrap scalar in a slice
	return []any{v}
}

// virtualStickyPin remembers the last virtual candidate that played
// successfully for a content key.
type virtualStickyPin struct {
	uri      string
	pinnedAt time.Time
	// generation is the write generation that produced this pin. A later
	// writer carrying an older generation must not replace it.
	generation uint64
}

// virtualStickyTTL bounds how long a pin can steer selection without being
// re-confirmed by another successful play.
const (
	virtualStickyTTL     = 24 * time.Hour
	virtualStickyMaxPins = 4096
)

// virtualRuntimePlausible reports whether a probed candidate's duration is
// consistent with the item's catalog runtime. The tolerance lives in
// scanner.VirtualRuntimeTolerance so the probe cache and the persist paths
// share one rule.
func virtualRuntimePlausible(probedSeconds, runtimeMinutes int) bool {
	return scanner.VirtualRuntimePlausible(probedSeconds, runtimeMinutes)
}

// logVirtualProbeSizeMismatch records a probe whose measured evidence
// contradicts the declared release — a duration or track-layout contradiction,
// never a size gap alone. It is distinct from the size-gap lines so the cause
// is obvious while debugging a provider's stale cache.
func logVirtualProbeSizeMismatch(ctx context.Context, fileID int, candidateURI string, declaredBytes, servedBytes int64) {
	slog.WarnContext(ctx, "background virtual probe rejected: measured identity contradicts the declared release",
		"component", "api", "file_id", fileID, "candidate_uri", candidateURI,
		"served_bytes", servedBytes, "declared_bytes", declaredBytes)
}

// logVirtualProbeSizeMismatchAccepted records a probe whose served container
// size does not match the row's declared release size but whose measured
// duration proves the content is the right one. The release was posted short
// of what the indexer advertised; the bytes are kept, and the warning keeps the
// posting discrepancy visible instead of hiding it.
func logVirtualProbeSizeMismatchAccepted(ctx context.Context, fileID int, candidateURI string, declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int) {
	slog.WarnContext(ctx, "background virtual probe accepted despite size mismatch",
		"component", "api", "file_id", fileID, "candidate_uri", candidateURI,
		"served_bytes", servedBytes, "declared_bytes", declaredBytes,
		"probed_duration_seconds", probedSeconds,
		"expected_runtime_minutes", expectedRuntimeMinutes)
}

// logVirtualProbeSizeGapV3 records a served size that disagrees with the
// declared release on a serve whose inventory is kept. An observation is not an
// acceptance: the bytes play, the row reports their measured inventory, and the
// gap is recorded for the pin, but the release identity stays unverified until a
// later serve confirms it. The line says so instead of claiming an identity the
// catalog never received.
func logVirtualProbeSizeGapV3(ctx context.Context, fileID int, candidateURI string, declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int, withholdsIdentity bool) {
	if scanner.VirtualServedSizeImplausible(declaredBytes, servedBytes) {
		slog.WarnContext(ctx, "virtual probe served a size beyond the plausibility ratio; inventory kept, release identity unverified",
			"component", "api", "file_id", fileID, "candidate_uri", candidateURI,
			"served_bytes", servedBytes, "declared_bytes", declaredBytes,
			"probed_duration_seconds", probedSeconds,
			"expected_runtime_minutes", expectedRuntimeMinutes)
		return
	}
	if withholdsIdentity {
		slog.WarnContext(ctx, "served size mismatch: probe inventory written, release identity unverified",
			"component", "api", "file_id", fileID, "candidate_uri", candidateURI,
			"served_bytes", servedBytes, "declared_bytes", declaredBytes,
			"probed_duration_seconds", probedSeconds,
			"expected_runtime_minutes", expectedRuntimeMinutes)
		return
	}
	logVirtualProbeSizeMismatchAccepted(ctx, fileID, candidateURI, declaredBytes, servedBytes, probedSeconds, expectedRuntimeMinutes)
}

// logVirtualProbeSiblingObservationPending records a same-release sibling whose
// served bytes are usable but do not yet establish the sibling pin's identity.
// The inventory is written metadata-only, so the line must not read as the
// identity rejection that names a wrong file.
func logVirtualProbeSiblingObservationPending(ctx context.Context, fileID int, candidateURI string, declaredBytes, servedBytes int64) {
	slog.InfoContext(ctx, "served size mismatch: sibling inventory written, release identity unverified",
		"component", "api", "file_id", fileID, "candidate_uri", candidateURI,
		"served_bytes", servedBytes, "declared_bytes", declaredBytes)
}

// peekVirtualSticky returns the pinned candidate URI for key, if fresh.
func (h *PlaybackHandler) peekVirtualSticky(key string) string {
	if h == nil || key == "" {
		return ""
	}
	h.virtualStickyMu.Lock()
	defer h.virtualStickyMu.Unlock()
	pin, ok := h.virtualStickyPins[key]
	if !ok {
		return ""
	}
	if time.Since(pin.pinnedAt) > virtualStickyTTL {
		delete(h.virtualStickyPins, key)
		return ""
	}
	return pin.uri
}

// pinVirtualSticky records uri as the sticky candidate for key. The map grows
// by one entry per distinct virtual content key; entries expire lazily on
// access, so no sweeper goroutine is needed.
func (h *PlaybackHandler) pinVirtualSticky(key, uri string) {
	h.pinVirtualStickyAt(key, uri, nextVirtualCacheGeneration())
}

// pinVirtualStickyAt is pinVirtualSticky with an explicit write generation. It
// refuses to replace a pin written by a newer generation, so a slow resolve
// that finishes after a newer one cannot steer later starts onto stale
// evidence. The generation is only an ordering token; it is never persisted.
func (h *PlaybackHandler) pinVirtualStickyAt(key, uri string, generation uint64) {
	if h == nil || key == "" || uri == "" {
		return
	}
	h.virtualStickyMu.Lock()
	defer h.virtualStickyMu.Unlock()
	if existing, ok := h.virtualStickyPins[key]; ok && existing.generation > generation {
		return
	}
	if h.virtualStickyPins == nil {
		h.virtualStickyPins = make(map[string]virtualStickyPin)
	}
	// Opportunistic hygiene on write: drop expired pins and bound the map so
	// long-lived processes cannot accumulate one entry per content key forever.
	now := time.Now()
	for k, pin := range h.virtualStickyPins {
		if now.Sub(pin.pinnedAt) > virtualStickyTTL {
			delete(h.virtualStickyPins, k)
		}
	}
	for len(h.virtualStickyPins) >= virtualStickyMaxPins {
		oldestKey := ""
		var oldest time.Time
		for k, pin := range h.virtualStickyPins {
			if oldestKey == "" || pin.pinnedAt.Before(oldest) {
				oldestKey, oldest = k, pin.pinnedAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(h.virtualStickyPins, oldestKey)
	}
	h.virtualStickyPins[key] = virtualStickyPin{uri: uri, pinnedAt: now, generation: generation}
}

// unpinVirtualSticky releases the pin for key when it still refers to uri,
// so a source that stopped working cannot steer later starts.
func (h *PlaybackHandler) unpinVirtualSticky(key, uri string) {
	if h == nil || key == "" || uri == "" {
		return
	}
	h.virtualStickyMu.Lock()
	defer h.virtualStickyMu.Unlock()
	if pin, ok := h.virtualStickyPins[key]; ok && pin.uri == uri {
		delete(h.virtualStickyPins, key)
	}
}

// applyVirtualStickyPin moves the pinned candidate to the front of the list
// while it is still offered, provided it meets score parity with the device-preferred
// candidate at index 0. If the pinned candidate has vanished or has a lower compatibility
// score for the requesting device, it is not promoted.
func (h *PlaybackHandler) applyVirtualStickyPin(key, pinnedURI string, candidates []VirtualPlaybackStream, device ...plugins.DeviceCapabilities) []VirtualPlaybackStream {
	if pinnedURI == "" || len(candidates) == 0 || virtualProviderOrderPreserved(candidates) {
		return candidates
	}
	pinnedIdx := -1
	for i, cand := range candidates {
		if cand.URI == pinnedURI {
			pinnedIdx = i
			break
		}
	}
	if pinnedIdx < 0 {
		// Pinned URI vanished from the offered set.
		h.unpinVirtualSticky(key, pinnedURI)
		return candidates
	}
	if pinnedIdx == 0 {
		return candidates
	}

	// Score parity guard: if device capabilities are present, only promote the pinned
	// candidate if its score is >= the device-ranked candidate at index 0.
	if len(device) > 0 {
		d := device[0]
		if len(d.CodecsVideo) > 0 || len(d.CodecsAudio) > 0 || d.HDR || d.DolbyVision || d.MaxResolution != "" {
			pinnedScore := plugins.ScoreCandidate(&candidates[pinnedIdx], d)
			topScore := plugins.ScoreCandidate(&candidates[0], d)
			if pinnedScore < topScore {
				return candidates
			}
		}
	}

	cand := candidates[pinnedIdx]
	reordered := make([]VirtualPlaybackStream, 0, len(candidates))
	reordered = append(reordered, cand)
	reordered = append(reordered, candidates[:pinnedIdx]...)
	reordered = append(reordered, candidates[pinnedIdx+1:]...)
	return reordered
}

// publishOfferedReleaseInventory serves the current same-row listing's probed
// track inventory to every live session still bound to a different candidate
// under the same neutral key. It is the menu-only answer to a vanished session
// pin: the provider dropped the pinned ?result= id, the session's transport
// keeps playing the already-open relay, and every rotation path correctly
// refuses to swap a different release into the playing row. Nothing then
// refreshed the menus, so they stayed empty until a stop/resume. The offered
// candidate's probed tracks are surfaced for display, keyed on the session's
// own playing identity, so a client never re-keys the transport.
//
// The evidence itself was refused a catalog write (the row cannot adopt a
// different release), and this never writes to the catalog either: it is an
// in-memory, best-effort push. A session that is not virtual, is not on the
// same neutral key, is already serving the offered candidate, or has no
// realtime connection is skipped. It returns how many sessions received the
// event.
func (h *PlaybackHandler) publishOfferedReleaseInventory(ctx context.Context, row *models.MediaFile, offeredURI string, probed *models.MediaFile) int {
	if h == nil || h.RealtimeHub == nil || row == nil || row.ID <= 0 || probed == nil || strings.TrimSpace(offeredURI) == "" {
		return 0
	}
	if len(probed.AudioTracks) == 0 && len(probed.SubtitleTracks) == 0 {
		// Nothing to show: an empty offered list would only clear a menu the
		// session may already hold. Leave the menus to the existing paths.
		return 0
	}
	lookup, ok := h.sessionMgr.(mediaFileSessionLookup)
	if !ok {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inventoryUpdatedPublishBudget)
	defer cancel()
	notified := 0
	for _, session := range lookup.GetSessionsByMediaFileID(row.ID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		if _, result := h.publishOfferedReleaseInventoryToSession(publishCtx, session, offeredURI, probed); result == inventoryPublishDelivered {
			notified++
		}
	}
	if notified > 0 {
		slog.InfoContext(ctx, "virtual offered-release inventory served to live session without a catalog write",
			"component", "api", "file_id", row.ID, "offered_uri", offeredURI, "sessions_notified", notified)
	}
	return notified
}

// publishOfferedReleaseInventoryToSession delivers one offered-release menu
// push to a single session. It re-reads the live binding under the same
// per-session lock the ordinary inventory publisher uses, so a rotation that
// landed while the caller enumerated sessions is observed and the offered data
// is dropped rather than delivered against a binding that already moved. The
// payload carries the session's bound row and leaves effective_virtual_uri
// empty, so folding it cannot move the transport.
func (h *PlaybackHandler) publishOfferedReleaseInventoryToSession(ctx context.Context, session *playback.Session, offeredURI string, probed *models.MediaFile) (string, inventoryPublishResult) {
	release := h.progressSideEffectLock(session.ID)
	defer release()
	live, err := h.sessionMgr.GetSession(session.ID)
	if err != nil || live == nil {
		return "", inventoryPublishObsolete
	}
	if !live.HasRealtimeConnection {
		return "", inventoryPublishObsolete
	}
	if !sameRowDifferentCandidate(live.VirtualSourceURI, offeredURI) {
		// The session already serves the offered candidate, or moved to
		// another row entirely: the display data is stale.
		return "", inventoryPublishObsolete
	}
	inventory := h.buildOfferedReleaseInventory(live, offeredURI, probed)
	event, err := playback.NewInventoryUpdatedEvent(live.ID, inventory)
	if err != nil {
		slog.WarnContext(ctx, "failed to encode offered-release realtime event",
			"component", "playback", "session", live.ID, "error", err)
		return inventory.InventoryRevision, inventoryPublishRetryable
	}
	if err := h.RealtimeHub.Send(live.ID, event); err != nil {
		if !errors.Is(err, playback.ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx, "failed to deliver offered-release realtime event",
				"component", "playback", "session", live.ID, "error", err)
			return inventory.InventoryRevision, inventoryPublishRetryable
		}
		return inventory.InventoryRevision, inventoryPublishObsolete
	}
	return inventory.InventoryRevision, inventoryPublishDelivered
}

// buildOfferedReleaseInventory shapes the offered candidate's probed tracks as
// a display-only live-inventory payload keyed on the session's bound row.
// effective_virtual_uri is deliberately left empty so no consumer mistakes the
// offer for the playing identity; the offered candidate travels only as
// OfferedVirtualURI. The revision is keyed on the offered URI so it is distinct
// from the playing source's own revision and the client's revision gate folds
// it exactly once.
func (h *PlaybackHandler) buildOfferedReleaseInventory(session *playback.Session, offeredURI string, probed *models.MediaFile) playback.PlaybackInventoryV3 {
	audioTracks := playback.AudioInventoryV3(probed)
	if audioTracks == nil {
		audioTracks = []playback.AudioInventoryItemV3{}
	}
	subtitleInventory := playback.BuildSubtitleInventoryV3(probed, nil)
	if subtitleInventory == nil {
		subtitleInventory = []playback.SubtitleInventoryItemV3{}
	}
	status := string(ProbeProvenanceVerified)
	revision := playback.ComputeInventoryRevisionV3(status, audioTracks, subtitleInventory, playback.InventorySourceIdentityV3{
		EffectiveMediaFileID:  session.MediaFileID,
		EffectiveVirtualURI:   offeredURI,
		VirtualSourceRevision: session.VirtualSourceRevision,
	})
	return playback.PlaybackInventoryV3{
		SessionID:            session.ID,
		InventoryRevision:    revision,
		InventoryStatus:      status,
		AudioTracks:          audioTracks,
		SubtitleInventory:    subtitleInventory,
		EffectiveMediaFileID: session.MediaFileID,
		// The offered release is deliberately not named as the bound candidate:
		// effective_virtual_uri stays empty so no consumer mistakes the offer
		// for the playing identity. The bound candidate travels only as the
		// session's own binding, which a client already holds.
		OfferedVirtualURI: offeredURI,
	}
}

func sameRowDifferentCandidate(bound, offered string) bool {
	bound, offered = strings.TrimSpace(bound), strings.TrimSpace(offered)
	if bound == "" || offered == "" {
		return false
	}
	if virtualPlaybackNeutralKey(bound) != virtualPlaybackNeutralKey(offered) {
		return false
	}
	return !sameVirtualCandidate(bound, offered)
}
