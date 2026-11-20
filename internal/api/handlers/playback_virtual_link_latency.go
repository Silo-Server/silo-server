package handlers

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	// copySeekAnchorResolveLatencyScale multiplies the link's slowest recent
	// provider observation into one copy-video seek-anchor resolve budget. A
	// resolve pays a pinned-relay lookup comparable to a provider listing and
	// then an FFmpeg probe over the same pipe, so twice the observed round trip
	// covers both legs; see copySeekAnchorResolveBudget.
	copySeekAnchorResolveLatencyScale = 2
	// virtualProviderLinkLatencyWindow bounds how long one observation keeps
	// sizing a link's budgets. The relay that was slow a minute ago is the same
	// relay this start will read, so a short window carries the measurement
	// across the client retry that follows a failed start without letting an
	// old stall size budgets indefinitely.
	virtualProviderLinkLatencyWindow = 2 * time.Minute
	// virtualProviderLinkLatencyLimit bounds the tracker. Entries expire on
	// record and read, so a large install cannot accumulate one per virtual
	// source owner forever.
	virtualProviderLinkLatencyLimit = 256
)

// copySeekAnchorSeekResolveTimeoutCap bounds how far a slow link may extend one
// copy-video seek-anchor resolve attempt. It stays well inside the replan
// budget (the caller's own deadline still bounds every attempt), and a relay
// that is stuck rather than slow must not hold the seek past it. The cap is a
// var so tests can shorten it.
var copySeekAnchorSeekResolveTimeoutCap = 20 * time.Second

// copySeekAnchorResolveBudget derives one seek-anchor resolve attempt's bound
// from the link's slowest recent provider observation. The floor is today's
// fixed budget (copySeekAnchorSeekResolveTimeout), so a link that answers
// inside it keeps exactly the old behavior; a healthy-but-slow relay — one
// whose own listings or anchor resolves recently took longer than the fixed
// budget — gets twice that observation instead, capped at maxBudget. The
// budget is never shortened below the floor.
func copySeekAnchorResolveBudget(floor, maxBudget, recent time.Duration) time.Duration {
	if floor <= 0 {
		floor = copySeekAnchorSeekResolveTimeout
	}
	if maxBudget < floor {
		maxBudget = floor
	}
	if recent <= 0 {
		return floor
	}
	scaled := copySeekAnchorResolveLatencyScale * recent
	if scaled <= floor {
		return floor
	}
	if scaled > maxBudget {
		return maxBudget
	}
	return scaled
}

// copySeekAnchorResolveBudgetFor returns the resolve budget for one attempt on
// this file's source. The observation is keyed on the provider link the source
// is served through, so a fast link is unaffected by another link's stall.
func copySeekAnchorResolveBudgetFor(file *models.MediaFile) time.Duration {
	return copySeekAnchorResolveBudget(
		copySeekAnchorSeekResolveTimeout,
		copySeekAnchorSeekResolveTimeoutCap,
		recentVirtualProviderLinkLatency(virtualProviderLinkLatencyKey(file)),
	)
}

// copySeekAnchorProbeBudgetHint reports the budget this source's own anchor
// probes imply. It is a var so a test can pin the sizing rule without paying for
// a real provider probe.
var copySeekAnchorProbeBudgetHint = playback.CopySeekAnchorProbeBudgetHint

// copySeekAnchorAdaptiveBudgetFor sizes one anchor attempt for this file. Two
// measurements feed it: the provider link's slowest recent round trip (what the
// resolve leg pays) and, when it is larger, the cost this source's own anchor
// probes recently paid. The second one exists because a probe is not a listing: it
// byte-seeks into the release and reads a frame on top of the provider open, so a
// link that lists in 3.6s can need 11.6s to anchor a deep position. Sizing only
// from the listing handed that probe the same too-small budget on every seek and
// forced the retry to discover the real cost again, which is the 5-19s start this
// fixes. A source with no probe history keeps exactly the budget it had.
func copySeekAnchorAdaptiveBudgetFor(file *models.MediaFile, ffmpegPath, sourceIdentity string) time.Duration {
	budget := copySeekAnchorResolveBudgetFor(file)
	if hint := copySeekAnchorProbeBudgetHint(ffmpegPath, sourceIdentity, playback.DefaultSegmentDuration); hint > budget {
		budget = hint
	}
	return budget
}

// copySeekProbeContext returns the context one copy-video seek-anchor probe runs
// under. The probe is the second leg of an attempt whose resolve already spent
// part of the attempt budget, so a probe bounded by the attempt context alone is
// truncated to whatever the resolve left — the production shape where a 0.7-1.9s
// probe died at about a second after a slow listing. A measured link therefore
// gets its own adaptive probe budget from the request context that cannot be
// shorter than the attempt's own window; the request deadline still bounds it. An
// unmeasured link, or a local file with no provider link, keeps the attempt
// context unchanged so a deliberately short bound is still honored.
func copySeekProbeContext(ctx, attemptCtx context.Context, observed time.Duration) (context.Context, context.CancelFunc) {
	if observed <= 0 {
		return attemptCtx, func() {}
	}
	budget := playback.AdaptiveProbeBudget(playback.RemoteProbeFloor, playback.CopySeekProbeTimeout, observed, 0)
	if budget <= 0 {
		return attemptCtx, func() {}
	}
	deadline := time.Now().Add(budget)
	if attemptDeadline, ok := attemptCtx.Deadline(); ok && attemptDeadline.After(deadline) {
		deadline = attemptDeadline
	}
	return context.WithDeadline(ctx, deadline)
}

// capProbeToAttempt keeps a probe inside its attempt's own window. A measured
// link's probe is allowed to outlive the attempt context (see
// copySeekProbeContext) because the resolve leg has already spent part of it,
// which is exactly what lets a slow resolve still leave a full probe. When the
// attempt's retry is reserved, that latitude would let the first probe run into
// the slice the retry needs and leave nothing for it — the production shape
// where one 10s provider open consumed the whole attempt budget. The attempt
// budget is sized from the same observation as the probe, so bounding the probe
// at it costs a measured link nothing it was promised.
func capProbeToAttempt(probeCtx context.Context, probeCancel context.CancelFunc, attemptCtx context.Context) (context.Context, context.CancelFunc) {
	attemptDeadline, ok := attemptCtx.Deadline()
	if !ok {
		return probeCtx, probeCancel
	}
	if probeDeadline, ok := probeCtx.Deadline(); ok && !probeDeadline.After(attemptDeadline) {
		return probeCtx, probeCancel
	}
	cappedCtx, cappedCancel := context.WithDeadline(probeCtx, attemptDeadline)
	return cappedCtx, func() {
		cappedCancel()
		probeCancel()
	}
}

// remoteProbeBudget sizes one probe attempt against file's provider link from
// that link's slowest recent observation and the caller's remaining budget. It
// is the handler-side entry to playback.AdaptiveProbeBudget: a link whose
// listing just took 4.8s gets a ~9.6s subtitle layout probe instead of the fixed
// 5s that killed it, while an unmeasured link keeps the floor and a caller with
// no time left gets 0. A nil or owner-less file has no provider link and keeps
// the floor.
func remoteProbeBudget(ctx context.Context, file *models.MediaFile, floor, maxBudget time.Duration) time.Duration {
	var remaining time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
		if remaining <= 0 {
			return 0
		}
	}
	return playback.AdaptiveProbeBudget(floor, maxBudget, recentVirtualProviderLinkLatency(virtualProviderLinkLatencyKey(file)), remaining)
}

// virtualProviderLinkLatencyKey names the provider link a source is served
// through. The virtual source owner installation is the identity relay
// resolution and provider listing already key on, and one slow owner answers
// slowly for every title and sibling release it lists. A local file has no
// provider link, so it never inherits another link's measurement.
func virtualProviderLinkLatencyKey(file *models.MediaFile) string {
	if file == nil || file.VirtualOwnerInstallationID <= 0 {
		return ""
	}
	return strconv.Itoa(file.VirtualOwnerInstallationID)
}

// virtualProviderLinkLatencyMark is the slowest provider observation a link
// served inside the window, stamped when it was observed.
type virtualProviderLinkLatencyMark struct {
	slowest    time.Duration
	observedAt time.Time
}

// virtualProviderLinkLatency remembers the slowest recent provider round trip
// per link. Two sources feed it: the resolver's provider listing durations (the
// same measurement the start path already attributes) and completed anchor
// resolve attempts on the link. The slow end is kept deliberately — the anchor
// start must fit the pipe the provider just proved it has, and a later faster
// answer must not retract that room. Tests may clear entries directly.
var virtualProviderLinkLatency = struct {
	sync.Mutex
	marks map[string]virtualProviderLinkLatencyMark
}{marks: make(map[string]virtualProviderLinkLatencyMark)}

// recordVirtualProviderLinkLatency records one observed provider round trip for
// key at now. A key with no provider link is ignored, and an observation only
// replaces the mark when it is slower, so the mark's own timestamp keeps the
// window anchored to the measurement that set it.
func recordVirtualProviderLinkLatency(key string, observed time.Duration, now time.Time) {
	if key == "" || observed <= 0 {
		return
	}
	virtualProviderLinkLatency.Lock()
	defer virtualProviderLinkLatency.Unlock()
	mark, ok := virtualProviderLinkLatency.marks[key]
	if ok && now.Sub(mark.observedAt) < virtualProviderLinkLatencyWindow && mark.slowest >= observed {
		return
	}
	if !ok && len(virtualProviderLinkLatency.marks) >= virtualProviderLinkLatencyLimit {
		for k, entry := range virtualProviderLinkLatency.marks {
			if now.Sub(entry.observedAt) >= virtualProviderLinkLatencyWindow {
				delete(virtualProviderLinkLatency.marks, k)
			}
		}
	}
	virtualProviderLinkLatency.marks[key] = virtualProviderLinkLatencyMark{slowest: observed, observedAt: now}
}

// recentVirtualProviderLinkLatency returns the slowest provider observation the
// link served inside the window, or 0 when nothing recent was measured. A
// lapsed mark is dropped on read so the map stays bounded.
func recentVirtualProviderLinkLatency(key string) time.Duration {
	if key == "" {
		return 0
	}
	virtualProviderLinkLatency.Lock()
	defer virtualProviderLinkLatency.Unlock()
	mark, ok := virtualProviderLinkLatency.marks[key]
	if !ok {
		return 0
	}
	if time.Since(mark.observedAt) >= virtualProviderLinkLatencyWindow {
		delete(virtualProviderLinkLatency.marks, key)
		return 0
	}
	return mark.slowest
}

const (
	// copySeekAnchorNoticeWindow bounds how long "this session is playing without
	// its anchor" is remembered. It only decides the level of a repeat notice, so
	// it may outlive the start that recorded it by as long as a session plausibly
	// seeks.
	copySeekAnchorNoticeWindow = time.Hour
	// copySeekAnchorNoticeLimit bounds the notice tracker. Entries expire on
	// record and read, so a busy server cannot accumulate one per session forever.
	copySeekAnchorNoticeLimit = 1024
)

// copySeekAnchorNotices remembers which session+source pairs have already been
// told that a start began without its copy seek anchor. A degraded start is
// deliberately not an error (the copied stream can begin at the requested
// position), which left it silent apart from a WARN per seek: an operator could
// not tell an anchor-less session from a resolved one, and the following seek had
// no record explaining why it paid another probe. The first notice per session is
// a WARN; repeats stay visible at INFO without repeating the warning.
var copySeekAnchorNotices = &copySeekAnchorNoticeTracker{seen: make(map[string]time.Time)}

type copySeekAnchorNoticeTracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

func (t *copySeekAnchorNoticeTracker) clock() time.Time {
	if t != nil && t.now != nil {
		return t.now()
	}
	return time.Now()
}

// first reports whether key has not been noticed inside the window and records
// it. The caller logs at WARN when it returns true and at INFO otherwise.
func (t *copySeekAnchorNoticeTracker) first(key string) bool {
	if t == nil || key == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[string]time.Time)
	}
	now := t.clock()
	for seenKey, observedAt := range t.seen {
		if now.Sub(observedAt) >= copySeekAnchorNoticeWindow {
			delete(t.seen, seenKey)
		}
	}
	if observedAt, ok := t.seen[key]; ok && now.Sub(observedAt) < copySeekAnchorNoticeWindow {
		t.seen[key] = now
		return false
	}
	if len(t.seen) >= copySeekAnchorNoticeLimit {
		t.seen = make(map[string]time.Time)
	}
	t.seen[key] = now
	return true
}

// copySeekAnchorNoticeKey names one session's anchor-less state for one source.
// The source is part of the key so a session that switches release is told about
// the new one, and a profile change inside the same session does not repeat it.
func copySeekAnchorNoticeKey(sessionID, sourceIdentity string) string {
	if sessionID == "" {
		return ""
	}
	return sessionID + "\x00" + sourceIdentity
}
