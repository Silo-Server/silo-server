package playback

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// A bitmap page ahead of its track's progressive artifact is answered retryably
// while the artifact is still parsing toward it. That answer is right for a page
// a few seconds ahead of the watermark, and it is a loop for a page far past it:
// the artifact demuxes the container from byte 0, so reaching a page at ten
// minutes of a multi-GB remux takes as long as reading that prefix. Nothing used
// to bound how long the same page could keep asking, so the client retried it
// until its own retry budget ran out (tens of retries, ~9 minutes) with no
// picture and no failure.
//
// This is the bound. Each playback session's wait on a track gets one recovery
// fill — the refusal that opens it says so, so the handler spawns exactly one —
// and one wait budget. The artifact may cross the page inside that budget and the
// retry is served from it (a page at a seek target on a slow relay commonly is).
// A fill that stops advancing, or a page still ahead when the budget is spent,
// gets a final non-retrying answer instead: the viewer sees the subtitle overlay
// report that it could not load, once, instead of a spinner that never ends.
const (
	// SubtitlePageUnavailableReason is the additive problem code for a bitmap
	// page the track's progressive artifact cannot reach inside its recovery
	// budget. Unlike the retryable page codes this one is final: the same page
	// and the same artifact cannot start answering sooner, and only a rebuilt
	// fetch (a seek, a track change, or a new session) begins a new wait.
	SubtitlePageUnavailableReason = "subtitle_page_unavailable"

	// subtitlePageRecoveryBudget bounds how long one session's page may wait
	// for the progressive artifact. It is not the page's extract budget (that
	// stays where it is); it covers the artifact's own from-zero demux, which
	// is what a page far into the file waits on. A 4K remux measured ~8x
	// realtime on the relay this was diagnosed on, so pages a few minutes in
	// land well inside it.
	//
	// The budget is not what a deep page needs on a slow relay: the same 4K
	// identity measured about 1x realtime (1-2 MB of .sup in 95 s), so a page
	// 20 minutes into the track is ~20 minutes of fill away and no budget a
	// client will hold the request for covers it. subtitlePageReachMargin is
	// what turns that measurement into the final answer early instead.
	subtitlePageRecoveryBudget = 90 * time.Second

	// subtitlePageReachMargin is how far past the remaining wait the fill's
	// measured reach must fall before the page is answered finally. A fill whose
	// rate would arrive at twice the remaining budget may speed up (the
	// transcode competing for the relay pauses), so only a shortfall this wide
	// is a verdict.
	subtitlePageReachMargin = 2.0

	// subtitlePageClientWaitBudget is how long the bitmap clients keep asking
	// for the same page. Their retry ladder backs off 5, 10, 20 and 40 s
	// (web/src/player/utils/subtitleWindows.ts), so a page asked for from the
	// first refusal is only being waited on for ~75 s in total — the viewer
	// sees the stalled card once the overlay stops promising a load, and the
	// remaining retries run behind it. The server's own 90 s budget is longer
	// than that, so on its own it answers after the client has already given
	// up: production's 1080p page (1074011 at 1080 s) spent the whole client
	// ladder on `page_not_ready` while the reach check compared the fill
	// against the server window and kept saying "retry".
	subtitlePageClientWaitBudget = 75 * time.Second

	// subtitlePageReachFloor is the shortest wait the reach check will judge a
	// page against. Once the client's ladder is spent, its remaining wait is
	// zero, but a fill that is about to cross the page (a few seconds of
	// artifact away) must still stay retryable: a viewer who presses Retry, or
	// whose client keeps polling, is then served rather than told the page is
	// unavailable a moment before it lands.
	subtitlePageReachFloor = 5 * time.Second

	// subtitlePageRecoveryStallWindow is how long the watermark may stay put
	// before the page stops waiting for it, once nothing is filling the track
	// any more. A real artifact advances between refusals; one that has not
	// moved in this long, with no fill left to move it, is ended or abandoned,
	// and the answer must not be "retry" forever.
	//
	// It exceeds the client's page retry ladder (5, 10, 20, 40 s between
	// attempts) on purpose. The window is measured from the last refusal, and a
	// client that is legitimately backing off must never look like a stalled
	// artifact: a 40 s gap alone used to trip a 20 s window and finalize a
	// healthy slow fill. The recovery budget stays the outer bound.
	subtitlePageRecoveryStallWindow = 45 * time.Second

	// subtitlePageRecoveryMaxEntries caps the wait ledger. Only pages that are
	// refused and not yet answered keep an entry; the cap only exists so
	// pathological churn cannot grow the map without bound.
	subtitlePageRecoveryMaxEntries = 1024
)

// subtitlePageRecovery is one playback session's wait for one track's
// progressive artifact.
type subtitlePageRecovery struct {
	first     time.Time
	lastSeen  time.Time
	lastCover float64
	// start is the page position that opened the wait. It is diagnostic only:
	// the budget belongs to the session's wait on the track, because a client
	// that re-anchors on every retry asks for a new start on the same artifact.
	start float64
	// page is the most recently refused page position. A refusal behind it, or
	// more than one page ahead, is a seek rather than the playhead crossing into
	// the next page during a retry, and starts a new wait (see pageWaitSeek).
	page float64
	// waiting holds the pages of this session still waiting on the track, keyed
	// by start, with the time each last refused. Each page's own first refusal
	// is the term the reach projection measures against, so a re-anchored page
	// inherits the session's budget without inheriting its ladder.
	waiting map[float64]subtitlePageWait
}

// subtitlePageWait is one page's place in its session's wait.
type subtitlePageWait struct {
	// first is when this page was first refused: the client's retry ladder and
	// the reach projection both start there, so a page that inherits the
	// session's budget still measures its own remaining wait.
	first time.Time
	// seen is when it last refused.
	seen time.Time
	// firstCover is the artifact watermark at the first refusal. The reach
	// projection measures the fill's average rate from here, so a burst
	// between two refusals cannot hide a page the fill will not reach: the
	// production page sat on `page_not_ready` for 75 s because the last interval
	// looked fast while the fill was still 20 s away.
	firstCover float64
}

// pageRecoveryRefusal records this page's wait and answers it: the retryable
// shape while the artifact may still reach the page, or the final unavailable
// shape once its budget is spent or it has stopped advancing. The retry shape's
// PageRecoveryDue is true on the first refusal only, which is the one request
// allowed to start the recovery fill.
func (c *SubtitleCache) pageRecoveryRefusal(opts StreamExtractOpts, start, covered float64, retryAfter time.Duration, retryReason string) error {
	wait, due, stalled, reachRate := c.notePageRecovery(opts, start, covered)
	if due {
		recordSubtitlePageRecoveryStarted()
	}
	if stalled || wait > c.pageRecoveryBudget() {
		recordSubtitlePageRecoveryUnavailable()
		// The measured numbers belong with the final answer: the wait is over
		// because the artifact cannot reach the page inside it, and the rate
		// that proves it is what a later budget change has to be based on.
		slog.Warn("subtitle page unavailable within its recovery budget",
			logKeyComponent, logValuePlayback,
			"start", start, "covered", covered, "waited", wait,
			"budget", c.pageRecoveryBudget(), "artifact_seconds_per_second", reachRate)
		// The fill is deliberately *not* canceled here. This answer is a
		// refusal, so the page's own bytes were never delivered: the fill is
		// still the only thing that can produce them, and its artifact and
		// watermark are the only copy of the progress it has made. Canceling it
		// is what made a deep page restart from byte zero on every request —
		// production abandoned the file's fill at 15 s and again at 95 s, each
		// answering the same refusal, neither reaching the page.
		//
		// Its extent is therefore the playback session that is waiting on it,
		// not this page's wait: the handler's abort ticker stops it on a seek or
		// when the session ends, and the cache's own warm timeout
		// (subtitleCacheWarmTimeout) is the outer bound. A later page of the
		// same track inherits the progress, which is the whole point.
		return &SubtitleExtractionUnavailableError{
			Reason:       SubtitlePageUnavailableReason,
			Start:        start,
			CoveredUntil: covered,
			Waited:       wait,
		}
	}
	return &SubtitleExtractionRetryError{
		RetryAfter:      retryAfter,
		Reason:          retryReason,
		CoveredUntil:    covered,
		PageRecoveryDue: due,
	}
}

func (c *SubtitleCache) pageRecoveryBudget() time.Duration {
	if c != nil && c.pageWaitBudget > 0 {
		return c.pageWaitBudget
	}
	return subtitlePageRecoveryBudget
}

func (c *SubtitleCache) pageRecoveryNow() time.Time {
	if c != nil && c.pageWaitNow != nil {
		return c.pageWaitNow()
	}
	return time.Now()
}

// notePageRecovery updates one session-and-track wait entry and reports how long
// it has waited, whether this is the first refusal of that wait, and whether the
// artifact has stopped advancing or has been measured too slow to reach the page
// inside the wait still left.
//
// The wait is keyed by the playback session and the track, not by the refused
// page position. A client re-anchors a few seconds forward on every
// bitmap-page retry, so a per-page key gave each retry a fresh budget: the fill
// spent its 90 s, the next page started a new wait, and the overlay sat on
// "loading" with no final answer — the production deep-4K page. The artifact is
// a per-track object, so a page at a different position of the same session
// inherits the wait the earlier page opened, which is what makes the final
// answer reachable. The session is part of the key because the wait is the
// viewer's, not the artifact's: a new session must pay its own measured wait
// rather than inherit a spent one (production carried waited=9m54 into a new
// session and answered its first page in 1 ms with covered=0).
func (c *SubtitleCache) notePageRecovery(opts StreamExtractOpts, start, covered float64) (wait time.Duration, due, stalled bool, reachRate float64) {
	key, ok := c.pageRecoveryKey(opts, start)
	if !ok {
		return 0, false, false, 0
	}
	now := c.pageRecoveryNow()
	// Whether a fill still holds this identity's slot decides the stall verdict,
	// so it is read before the ledger lock. A running fill is the thing that
	// could still cross the page: a page whose coverage has not moved while
	// something is still filling it is slow, not dead, and the recovery budget
	// bounds it.
	filling := c.progressiveFillInFlight(opts)
	c.pageWaitMu.Lock()
	defer c.pageWaitMu.Unlock()
	if c.pageWaits == nil {
		c.pageWaits = make(map[string]subtitlePageRecovery)
	}
	entry, seen := c.pageWaits[key]
	// A seek opens a new wait for the page it lands on: it is not the same
	// client still retrying the same page against the same artifact. Within one
	// wait the budget stays per session and track, which is what keeps a
	// re-anchoring retry from getting a fresh budget each time.
	if !seen || pageWaitSeek(entry.page, start, opts) {
		if len(c.pageWaits) >= subtitlePageRecoveryMaxEntries {
			c.prunePageRecoveryLocked(now)
		}
		c.pageWaits[key] = subtitlePageRecovery{
			first: now, lastSeen: now, lastCover: covered, start: start, page: start,
			waiting: map[float64]subtitlePageWait{
				start: {first: now, seen: now, firstCover: covered},
			},
		}
		return 0, true, false, 0
	}
	// Record this page as waiting before the verdicts. The page keeps its own
	// first refusal even when the session's wait opened earlier: both the
	// client's ladder and the reach rate are measured from there.
	page, pageSeen := entry.waiting[start]
	if !pageSeen {
		page = subtitlePageWait{first: now, firstCover: covered}
	}
	page.seen = now
	entry.waiting[start] = page
	entry.page = start
	entry.start = start
	entry.pruneWaitingLocked(now)
	wait = now.Sub(entry.first)
	stalled = !filling && covered <= entry.lastCover && now.Sub(entry.lastSeen) >= subtitlePageRecoveryStallWindow
	// A fill that ended without committing since this track began its wait is
	// not going to move the coverage any further. When the reason is final for
	// this page — the fill spent the budget the wait gave it — repeating the
	// read cannot reach the page sooner, so the page ends now instead of after
	// another stall window.
	if !filling && c.fillEndedFinal(opts, entry.first) {
		stalled = true
	}
	// What reaching this page costs is measurable once the fill has advanced:
	// the media seconds it parsed since this page's first refusal, over the wall
	// seconds since it. A page the fill cannot reach inside the wait still left,
	// by a wide margin, can never be answered by retrying, so the final answer
	// is due now rather than after the budget — the production deep 4K page
	// spent 95 s on a fill that was parsing at about real time toward a page
	// twenty minutes ahead. The margin keeps a merely tight estimate retryable:
	// fills speed up when the transcode competing for the relay pauses, so only
	// an order-of-magnitude shortfall is a verdict.
	//
	// Measuring from the page's first refusal is deliberate. The last interval
	// alone is not the fill's rate: production's 1080p page sat on
	// `page_not_ready` through the whole 75 s ladder because the watermark
	// happened to jump between two refusals while the fill was still ~20 s from
	// the page. The page's own ladder is the other half of the same measurement
	// (see pageReachVerdict).
	clientRemaining := subtitlePageClientWaitBudget - now.Sub(page.first)
	if clientRemaining < subtitlePageReachFloor {
		clientRemaining = subtitlePageReachFloor
	}
	if serverRemaining := c.pageRecoveryBudget() - wait; serverRemaining < clientRemaining {
		clientRemaining = serverRemaining
	}
	if !stalled && filling && covered > page.firstCover && clientRemaining > 0 {
		reachRate, stalled = pageReachVerdict(now.Sub(page.first), page.firstCover, covered, start, clientRemaining)
	}
	entry.lastSeen = now
	entry.lastCover = covered
	c.pageWaits[key] = entry
	return wait, false, stalled, reachRate
}

// pageWaitSeek reports whether start is a seek away from the page the track's
// wait is following rather than the playhead crossing into a later page during
// a retry. The client re-anchors a page request on the playhead, and a 75 s
// retry ladder can advance up to two 120 s pages even at double playback speed;
// backwards, or further ahead than that, is the viewer seeking, and the page it
// lands on pays its own bounded wait instead of inheriting the one it left.
// Without this a seek inherited a spent budget and was answered
// `subtitle_page_unavailable` in 1 ms with covered=0.
func pageWaitSeek(previous, start float64, opts StreamExtractOpts) bool {
	if start < previous-1 {
		return true
	}
	step := SubtitleRequestGridFor(opts.SourceCodec).PageSeconds
	if step <= 0 {
		step = SubtitleBitmapPageSeconds
	}
	return start > previous+2*step+1
}

// pageReachVerdict reports how fast the fill is parsing and whether the page it
// is heading for is out of reach. The wait that decides "too slow" is the
// client's, not the server's: a page nobody asks for any more cannot be served
// by retrying, whatever the fill's own budget allows. remaining is already the
// shorter of the client's ladder still left and the server's budget, floored at
// its first retry step so a fill that is about to cross the page keeps the page
// retryable even after the ladder is spent. The verdict needs the margin
// because a fill speeds up whenever the transcode competing for the relay
// pauses.
func pageReachVerdict(elapsed time.Duration, previouslyCovered, covered, start float64, remaining time.Duration) (rate float64, outOfReach bool) {
	if elapsed <= 0 {
		return 0, false
	}
	rate = (covered - previouslyCovered) / elapsed.Seconds()
	if rate <= 0 || remaining <= 0 {
		return rate, false
	}
	return rate, (start-covered)/rate > subtitlePageReachMargin*remaining.Seconds()
}

// pruneWaitingLocked forgets pages that have stopped asking. A page's own
// ladder is 75 s: a position that has not refused within it is not the page the
// client is waiting on any more, so keeping it would only make the reach
// projection measure an abandoned position. Caller holds pageWaitMu.
func (r *subtitlePageRecovery) pruneWaitingLocked(now time.Time) {
	for position, wait := range r.waiting {
		if now.Sub(wait.first) > subtitlePageClientWaitBudget {
			delete(r.waiting, position)
		}
	}
}

// progressiveFillInFlight reports whether a progressive .sup fill currently holds
// this identity's slot — the request path's, a warm's, or another request's.
func (c *SubtitleCache) progressiveFillInFlight(opts StreamExtractOpts) bool {
	if c == nil {
		return false
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inflight[key] != nil
}

// clearPageRecovery forgets a track's waits once a page of it has been answered,
// so a later visit pays its own bounded wait instead of inheriting one.
func (c *SubtitleCache) clearPageRecovery(opts StreamExtractOpts, _ float64) {
	fillKey, ok := c.pageRecoveryFillKey(opts)
	if !ok {
		return
	}
	c.pageWaitMu.Lock()
	for key := range c.pageWaits {
		if strings.HasPrefix(key, pageRecoveryFillPrefix(fillKey)) {
			delete(c.pageWaits, key)
		}
	}
	c.pageWaitMu.Unlock()
}

// pageRecoveryKey names one session's wait on one track. Neither the page
// position nor the session is a per-page key: see notePageRecovery.
func (c *SubtitleCache) pageRecoveryKey(opts StreamExtractOpts, _ float64) (string, bool) {
	fillKey, ok := c.pageRecoveryFillKey(opts)
	if !ok {
		return "", false
	}
	return pageRecoveryScopeKey(fillKey, opts.PageWaitScope), true
}

// pageRecoveryScopeKey scopes one track's wait to the playback session that
// opened it. The fill key never contains a NUL byte, so the prefix scan in
// clearPageRecovery cannot match another track.
func pageRecoveryScopeKey(fillKey, scope string) string {
	return pageRecoveryFillPrefix(fillKey) + scope
}

// pageRecoveryFillPrefix is the prefix every session's wait on this track
// shares: the fill identity plus the scope separator.
func pageRecoveryFillPrefix(fillKey string) string {
	return fillKey + "\x00"
}

// pageRecoveryFillKey is the page ledger's per-track key: the same identity the
// track's fill admits and records its end under.
func (c *SubtitleCache) pageRecoveryFillKey(opts StreamExtractOpts) (string, bool) {
	return c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
}

// fillEndedFinal reports whether this track's fill ended, at or after since, for
// a reason that is final for a page waiting on it: the fill ran out the budget it
// was given (a caller's, or the cache's warm timeout), so the same read at the
// same rate cannot reach the page any sooner. Transient ends (a provider
// failure, a session end, a yield) leave the page retrying inside its own budget
// instead of turning them into a verdict.
func (c *SubtitleCache) fillEndedFinal(opts StreamExtractOpts, since time.Time) bool {
	key, ok := c.pageRecoveryFillKey(opts)
	if !ok {
		return false
	}
	return c.fillEndedSince(key, since) == subtitleFillEndPageBudgetSpent
}

// subtitleFillEndPageBudgetSpent is the fill-end reason recorded when a
// page-driven fill ran out the budget it was given — the waiter's, or the
// cache's warm timeout when the fill outlives the page's own wait.
const subtitleFillEndPageBudgetSpent = "page_wait_budget_spent"

// prunePageRecoveryLocked drops entries that already waited past the budget this
// cache applies. Their pages get a fresh, bounded wait the next time they ask.
// Caller holds pageWaitMu.
func (c *SubtitleCache) prunePageRecoveryLocked(now time.Time) {
	budget := c.pageRecoveryBudget()
	for key, entry := range c.pageWaits {
		if now.Sub(entry.first) > budget {
			delete(c.pageWaits, key)
		}
	}
	if len(c.pageWaits) < subtitlePageRecoveryMaxEntries {
		return
	}
	// Still at the cap (a burst of distinct pages inside one budget): drop the
	// oldest wait. That page simply starts over; nothing overstates coverage.
	oldestKey, oldest := "", now
	for key, entry := range c.pageWaits {
		if entry.first.Before(oldest) {
			oldestKey, oldest = key, entry.first
		}
	}
	if oldestKey != "" {
		delete(c.pageWaits, oldestKey)
	}
}

// SetPageRecoveryBudgetForTest overrides the progressive-artifact wait budget so
// a test can exercise the bounded answer without waiting it out. A zero or
// negative budget restores the production one.
func (c *SubtitleCache) SetPageRecoveryBudgetForTest(budget time.Duration) {
	if c == nil {
		return
	}
	c.pageWaitBudget = budget
}

// SubtitleExtractionUnavailableError is the final answer for a bitmap page the
// track's progressive artifact cannot reach. It is not retryable: repeating the
// same page against the same artifact pays the same prefix again, which is the
// loop this type ends. The handler answers it once and the client ends the
// track's fetch loop.
type SubtitleExtractionUnavailableError struct {
	Reason       string
	Start        float64
	CoveredUntil float64
	Waited       time.Duration
}

func (e *SubtitleExtractionUnavailableError) Error() string {
	return fmt.Sprintf("subtitle page at %.3fs is unavailable: the track's progressive artifact covers %.3fs after waiting %s (%s)",
		e.Start, e.CoveredUntil, e.Waited.Round(time.Millisecond), e.Reason)
}

// IsSubtitlePageUnavailable reports whether err is the final page answer.
func IsSubtitlePageUnavailable(err error) bool {
	var unavailable *SubtitleExtractionUnavailableError
	return errors.As(err, &unavailable)
}
