package playback

import (
	"errors"
	"fmt"
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
	subtitlePageRecoveryBudget = 90 * time.Second

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

// subtitlePageRecovery is one page's wait for its track's progressive artifact.
type subtitlePageRecovery struct {
	first     time.Time
	lastSeen  time.Time
	lastCover float64
}

// pageRecoveryRefusal records this page's wait and answers it: the retryable
// shape while the artifact may still reach the page, or the final unavailable
// shape once its budget is spent or it has stopped advancing. The retry shape's
// PageRecoveryDue is true on the first refusal only, which is the one request
// allowed to start the recovery fill.
func (c *SubtitleCache) pageRecoveryRefusal(opts StreamExtractOpts, start, covered float64, retryAfter time.Duration, retryReason string) error {
	wait, due, stalled := c.notePageRecovery(opts, start, covered)
	if due {
		recordSubtitlePageRecoveryStarted()
	}
	if stalled || wait > c.pageRecoveryBudget() {
		recordSubtitlePageRecoveryUnavailable()
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

// notePageRecovery updates one page's wait entry and reports how long it has
// waited, whether this is its first refusal, and whether the artifact has
// stopped advancing.
func (c *SubtitleCache) notePageRecovery(opts StreamExtractOpts, start, covered float64) (wait time.Duration, due, stalled bool) {
	key, ok := c.pageRecoveryKey(opts, start)
	if !ok {
		return 0, false, false
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
		c.pageWaits[key] = subtitlePageRecovery{first: now, lastSeen: now, lastCover: covered}
		return 0, true, false
	}
	wait = now.Sub(entry.first)
	stalled = !filling && covered <= entry.lastCover && now.Sub(entry.lastSeen) >= subtitlePageRecoveryStallWindow
	entry.lastSeen = now
	entry.lastCover = covered
	c.pageWaits[key] = entry
	return wait, false, stalled
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

// clearPageRecovery forgets a page's wait once it has been answered, so a later
// visit pays its own bounded wait instead of inheriting this one.
func (c *SubtitleCache) clearPageRecovery(opts StreamExtractOpts, start float64) {
	key, ok := c.pageRecoveryKey(opts, start)
	if !ok {
		return
	}
	c.pageWaitMu.Lock()
	delete(c.pageWaits, key)
	c.pageWaitMu.Unlock()
}

func (c *SubtitleCache) pageRecoveryKey(opts StreamExtractOpts, start float64) (string, bool) {
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s|%.3f", key, start), true
}

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
