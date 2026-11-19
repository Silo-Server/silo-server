package playback

import (
	"errors"
	"fmt"
	"log/slog"
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
// This is the bound. Each page gets one recovery fill — the request that starts
// it says so, so the handler spawns exactly one — and one wait budget. The
// artifact may cross the page inside that budget and the retry is served from it
// (a page at a seek target on a slow relay commonly is). A fill that stops
// advancing, or a page still ahead when the budget is spent, gets a final
// non-retrying answer instead: the viewer sees the subtitle overlay report that
// it could not load, once, instead of a spinner that never ends.
const (
	// SubtitlePageUnavailableReason is the additive problem code for a bitmap
	// page the track's progressive artifact cannot reach inside its recovery
	// budget. Unlike the retryable page codes this one is final: the same page
	// and the same artifact cannot start answering sooner, and only a rebuilt
	// fetch (a seek, a track change, or a new session) begins a new wait.
	SubtitlePageUnavailableReason = "subtitle_page_unavailable"

	// subtitlePageRecoveryBudget bounds how long one page may wait for the
	// progressive artifact. It is not the page's extract budget (that stays
	// where it is); it covers the artifact's own from-zero demux, which is what
	// a page far into the file waits on. A 4K remux measured ~8x realtime on the
	// relay this was diagnosed on, so pages a few minutes in land well inside it.
	//
	// The budget is not what a deep page needs on a slow relay: the same 4K
	// identity measured about 1x realtime (1-2 MB of .sup in 95 s), so a page
	// 20 minutes into the track is ~20 minutes of fill away and no budget a
	// client will hold the request for covers it. subtitlePageReachMargin is
	// what turns that measurement into the final answer early instead.
	subtitlePageRecoveryBudget = 90 * time.Second

	// subtitlePageReachMargin is how far past the remaining wait the fill's
	// measured reach must fall before the page is answered finally. A fill whose
	// rate would arrive at 1.5x the remaining budget may speed up (the
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

// subtitlePageRecovery is one track's wait for its progressive artifact.
type subtitlePageRecovery struct {
	first     time.Time
	lastSeen  time.Time
	lastCover float64
	// start is the page position that opened the wait. It is diagnostic only:
	// the budget belongs to the track, because a client that re-anchors on every
	// retry asks for a new start on the same artifact.
	start float64
	// waiting holds the page positions still waiting on this track, keyed by
	// start, with the time each last refused. A final answer reads it to decide
	// whether the fill it cancels still has another page to serve: two clients,
	// or a seek target and the playhead page, can wait on one track.
	waiting map[float64]time.Time
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
		// Nobody is going to ask for this page again, so the fill is now a
		// read for no one. Cancel it unless another page of the track is still
		// waiting on it; production's answered page ran on to 95 s and 3.1 MB
		// past its final answer.
		c.cancelFillAfterFinalPage(opts, start)
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

// notePageRecovery updates one track's wait entry and reports how long it has
// waited, whether this is the first refusal of that wait, and whether the
// artifact has stopped advancing or has been measured too slow to reach the page
// inside the wait still left.
//
// The wait is keyed by the track, not by the refused page position. A client
// re-anchors a few seconds forward on every bitmap-page retry, so a per-page
// key gave each retry a fresh budget: the fill spent its 90 s, the next page
// started a new wait, and the overlay sat on "loading" with no final answer —
// the production deep-4K page. The artifact is a per-track object, so its wait
// budget is too; a page at a different position of the same track inherits the
// wait the earlier page opened, which is what makes the final answer reachable.
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
	if !seen {
		if len(c.pageWaits) >= subtitlePageRecoveryMaxEntries {
			c.prunePageRecoveryLocked(now)
		}
		c.pageWaits[key] = subtitlePageRecovery{
			first: now, lastSeen: now, lastCover: covered, start: start,
			waiting: map[float64]time.Time{start: now},
		}
		return 0, true, false, 0
	}
	// Record this page as waiting before the verdicts, so a final answer in this
	// same call can tell whether any other page still needs the fill.
	entry.noteWaiting(now, start)
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
	// the media seconds it parsed between two refusals, over the wall seconds
	// between them. A page the fill cannot reach inside the wait still left, by
	// a wide margin, can never be answered by retrying, so the final answer is
	// due now rather than after the budget — the production deep 4K page spent
	// 95 s on a fill that was parsing at about real time toward a page twenty
	// minutes ahead. The margin keeps a merely tight estimate retryable: fills
	// speed up when the transcode competing for the relay pauses, so only an
	// order-of-magnitude shortfall is a verdict.
	//
	// The wait that decides "too slow" is the client's, not the server's (see
	// pageReachVerdict).
	if !stalled && filling && covered > entry.lastCover {
		reachRate, stalled = pageReachVerdict(now.Sub(entry.lastSeen), entry.lastCover, covered, start, wait, c.pageRecoveryBudget())
	}
	entry.lastSeen = now
	entry.lastCover = covered
	if wait == 0 && start != entry.start {
		// A different page of the same track, still inside the opening wait's
		// window: remember it only for the diagnostics line.
		entry.start = start
	}
	c.pageWaits[key] = entry
	return wait, false, stalled, reachRate
}

// pageReachVerdict reports how fast the fill is parsing and whether the page it
// is heading for is out of reach. The wait that decides "too slow" is the
// client's: a page nobody asks for any more cannot be served by retrying,
// whatever the fill's own budget allows. The projection is measured against the
// shorter of the two windows, with the client's side floored at its first retry
// step so a fill that is about to cross the page keeps the page retryable even
// after the ladder is spent, and the verdict needs the margin because a fill
// speeds up whenever the transcode competing for the relay pauses.
func pageReachVerdict(elapsed time.Duration, previouslyCovered, covered, start float64, waited, budget time.Duration) (rate float64, outOfReach bool) {
	if elapsed <= 0 {
		return 0, false
	}
	rate = (covered - previouslyCovered) / elapsed.Seconds()
	if rate <= 0 {
		return rate, false
	}
	remaining := budget - waited
	clientRemaining := subtitlePageClientWaitBudget - waited
	if clientRemaining < subtitlePageReachFloor {
		clientRemaining = subtitlePageReachFloor
	}
	if remaining < clientRemaining {
		clientRemaining = remaining
	}
	if clientRemaining <= 0 {
		return rate, false
	}
	return rate, (start-covered)/rate > subtitlePageReachMargin*clientRemaining.Seconds()
}

// noteWaiting records that the page at start refused now, and forgets pages
// that have stopped asking. A page that has not refused for the client's own
// wait budget is not waiting any more: its client gave up or seeked away, and
// its fill must not be kept alive on its behalf.
func (r *subtitlePageRecovery) noteWaiting(now time.Time, start float64) {
	if r.waiting == nil {
		r.waiting = make(map[float64]time.Time, 1)
	}
	r.waiting[start] = now
	for position, seen := range r.waiting {
		if now.Sub(seen) > subtitlePageClientWaitBudget {
			delete(r.waiting, position)
		}
	}
}

// cancelFillAfterFinalPage stops the fill this page was waiting on once the page
// has its final answer, unless another page of the same track is still waiting
// for it. The fill's remaining work cannot serve the answered page, and
// production's answered page left it reading to 95 s and 3.1 MB for nobody.
func (c *SubtitleCache) cancelFillAfterFinalPage(opts StreamExtractOpts, start float64) {
	key, ok := c.pageRecoveryFillKey(opts)
	if !ok {
		return
	}
	now := c.pageRecoveryNow()
	c.pageWaitMu.Lock()
	if entry, seen := c.pageWaits[key]; seen {
		delete(entry.waiting, start)
		for position, seenAt := range entry.waiting {
			if now.Sub(seenAt) > subtitlePageClientWaitBudget {
				delete(entry.waiting, position)
			}
		}
		c.pageWaits[key] = entry
		if len(entry.waiting) > 0 {
			// Another page of this track is still waiting: the fill is its read,
			// not this page's.
			c.pageWaitMu.Unlock()
			return
		}
	}
	c.pageWaitMu.Unlock()
	c.cancelTrackFill(key)
}

// cancelTrackFill stops the full-track fill registered for key, if any. The
// cause is the page-answered one rather than the page-preemption one: this fill
// is finished, not yielding to a page that will hand it back.
func (c *SubtitleCache) cancelTrackFill(key string) {
	if c == nil || key == "" {
		return
	}
	c.windowMu.Lock()
	run := c.warmRuns[key]
	c.windowMu.Unlock()
	if run != nil {
		run.cancel(errSubtitleFillPageAnswered)
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

// clearPageRecovery forgets a track's wait once a page of it has been answered,
// so a later visit pays its own bounded wait instead of inheriting this one.
func (c *SubtitleCache) clearPageRecovery(opts StreamExtractOpts, _ float64) {
	key, ok := c.pageRecoveryKey(opts, 0)
	if !ok {
		return
	}
	c.pageWaitMu.Lock()
	delete(c.pageWaits, key)
	c.pageWaitMu.Unlock()
}

// pageRecoveryKey names one track's wait. The page position is deliberately not
// part of the key: see notePageRecovery.
func (c *SubtitleCache) pageRecoveryKey(opts StreamExtractOpts, _ float64) (string, bool) {
	return c.pageRecoveryFillKey(opts)
}

// pageRecoveryFillKey is the page ledger's per-track key: the same identity the
// track's fill admits and records its end under.
func (c *SubtitleCache) pageRecoveryFillKey(opts StreamExtractOpts) (string, bool) {
	return c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
}

// fillEndedFinal reports whether this track's fill ended, at or after since, for
// a reason that is final for a page waiting on it: the fill spent the budget the
// wait gave it, so the same read at the same rate cannot reach the page any
// sooner. Transient ends (a provider failure, a session end, a yield) leave the
// page retrying inside its own budget instead of turning them into a verdict.
func (c *SubtitleCache) fillEndedFinal(opts StreamExtractOpts, since time.Time) bool {
	key, ok := c.pageRecoveryFillKey(opts)
	if !ok {
		return false
	}
	return c.fillEndedSince(key, since) == subtitleFillEndPageBudgetSpent
}

// subtitleFillEndPageBudgetSpent is the fill-end reason recorded when a
// page-driven fill ran out the budget its waiter gave it.
const subtitleFillEndPageBudgetSpent = "page_wait_budget_spent"

// subtitleFillEndPageAnswered is the fill-end reason recorded when the page a
// fill was serving got its final answer and nothing else was waiting on the
// track: the fill is stopped instead of reading the artifact out for no one.
const subtitleFillEndPageAnswered = "page_answered_finally"

// errSubtitleFillPageAnswered cancels a full-track fill whose page has already
// been answered. It is deliberately not ErrSubtitleWarmCanceled: that cause
// means an admitted page took the pipe and the fill must resume afterwards,
// while this one ends the fill for good.
var errSubtitleFillPageAnswered = errors.New("subtitle fill ended: its page was answered finally")

// SubtitlePageRecoveryBudget is the production wait budget for one refused
// bitmap page. A caller that starts the fill the page is waiting on bounds that
// fill with this (plus a little slack), so the fill ends when the wait does
// instead of holding a warm slot for a page nobody is waiting on any more.
func SubtitlePageRecoveryBudget() time.Duration { return subtitlePageRecoveryBudget }

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
