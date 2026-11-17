package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A virtual start pays for two things in sequence: it resolves a candidate
// (provider listing and staging, the attempt loop and its probe, the
// alternate-version walk) and then it starts the transport and waits for its
// first playable manifest. The phase is the sum of those bounds, so it covers
// what it contains: a resolve that spends its whole allowance still leaves the
// readiness wait its own full window, and the window is never silently
// shortened by a slow listing.
//
//	phase = resolve (13 s) + max(readiness 30 s, auto walk 29 s) = 43 s
//
// One explicit cap still owns the whole start, so nothing waits forever and the
// completed-release pin retry only sees what the phase has left.
//
// A viewer can therefore wait up to 43 s for a start that is going to fail,
// where the 30 s phase it replaces answered after 30 s — with a resolve that
// had already eaten the readiness window, which is how a healthy hw_accel=auto
// start terminalised as transcode_start_failed. 43 s stays inside the
// first-party web player's 45 s per-attempt start abort
// (web/src/player/start-v2.ts), so the answer is still delivered rather than
// racing the client's abort. A healthy start approaches neither number: the
// resolve is a cache hit once discovery has seeded the listing, and the
// transport reaches readiness in a few seconds.
//
// The budget is a var so tests can shrink it.
var virtualStartPhaseBudget = virtualStartResolveBudget + max(virtualStartReadinessBudget, localAutoStartupBudgetV3)

// virtualStartResolveBudget is how long catalog resolve work may run inside the
// phase: the provider listing/staging window plus the reserve for the
// start-path probe that follows it. Reserving the probe keeps a slow listing
// from leaving it a zero-length window (the pre-empted `bound_ms=0` shape).
var virtualStartResolveBudget = virtualStartListingBudget + virtualStartProbeBudget

// virtualStartListingBudget bounds provider listing and candidate staging inside
// a start's resolve. Discovery's detached read now seeds the listing for the
// play that follows, so a start normally resolves from cache; this window
// covers the start that must list on its own (a direct deep link, or a
// discovery that failed). Provider listings measured 17–25 s on the deployment
// the trace came from, and the resolve's own listing cap
// (virtualListingStageBudget) already cut them at 15 s before seeding, so a
// 10 s start window changes no verdict: such a start now fails retryably 5 s
// sooner instead of spending the readiness window on the listing.
var virtualStartListingBudget = 10 * time.Second

// virtualStartReadinessBudget is one full first-manifest window — the same
// bound the local transport waits under (playback.ManifestStartupTimeout), so
// the phase never hands the readiness wait less than the transport's own
// contract allows.
var virtualStartReadinessBudget = playback.ManifestStartupTimeout

// virtualStartClientBudget is the longest a first-party client waits for one
// start request before it aborts it (web/src/player/start-v2.ts,
// START_TIMEOUT_MS). A phase longer than this is a lie in the other direction:
// the answer would never reach the client, and the client's retry would restart
// the work the phase was still holding. It is pinned by
// TestVirtualStartPhaseBudgetCoversWhatItContains so raising a stage budget
// forces a deliberate decision about the client's wait.
var virtualStartClientBudget = 45 * time.Second

// virtualPinRetryMinBudget is the smallest remaining start budget worth
// spending on the same-release retry. Below it the start returns through the
// ordinary alternate walk instead of opening another provider round-trip that
// the shared deadline would cut off anyway. A var so tests can exercise the
// boundary without sleeping.
var virtualPinRetryMinBudget = 5 * time.Second

// virtualResolveAttemptBudget derives one virtual resolve attempt's bound from
// the request's remaining time under an explicit cap:
//
//	budget = min(maxBudget, time.Until(ctx deadline))
//
// The result is always positive; ok=false means the request has no time left to
// spend on the attempt. A context built with a bare WithTimeout on an
// already-consumed parent deadline reports a negative bound (the production
// `bound_ms=-34` series), so every wait inside it is pre-empted before it starts
// and the resolve immediately fails with context deadline exceeded — which the
// version fallback then read as a provider listing failure and used to justify a
// release substitution. Deriving the bound here keeps that decision with the
// caller, which can answer a truthful retryable terminal instead.
func virtualResolveAttemptBudget(ctx context.Context, maxBudget time.Duration) (time.Duration, bool) {
	if maxBudget <= 0 {
		return 0, false
	}
	if ctx == nil {
		return maxBudget, true
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return maxBudget, true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, false
	}
	return min(maxBudget, remaining), true
}

// virtualResolveRemainingMilliseconds reports the request's remaining time for
// logs. It never returns a negative duration: an expired budget is 0 ms left,
// and the caller names that state in its own words instead of logging a wait
// that already happened.
func virtualResolveRemainingMilliseconds(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	return max(0, time.Until(deadline).Milliseconds())
}

// withVirtualStartPhaseDeadline attaches the shared start-phase deadline. The
// caller owns the returned cancel and must defer it for the duration of the
// start, so transport startup and every recovery stage see the same bound.
func withVirtualStartPhaseDeadline(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, budget)
}

// virtualStartProbeReserve is how much of a resolve's budget the listing stage
// may not take, so the probe that follows the listing keeps its own bound. It
// is the probe's own budget, capped at half the resolve: a resolve smaller than
// twice the probe bound (a shrunk test budget, or an alternate walk's
// per-candidate window) keeps the historical half-and-half split instead of
// handing the listing nothing at all.
func virtualStartProbeReserve(startPath bool, resolveBudget time.Duration) time.Duration {
	return min(virtualResolveProbeBudget(startPath), max(0, resolveBudget/2))
}

// virtualStartReadinessWait reports the deadline the transport's readiness step
// is handed: its own window, clamped only by what the phase has left. Because
// the phase is resolve + readiness, a resolve that spent its entire allowance
// still leaves the full window — the property the pre-fix 30 s phase could not
// hold, where a 20 s resolve left the readiness wait 10 s. A start that overran
// the phase itself gets 0, which the caller reports as an exhausted budget
// rather than as a transport verdict.
func virtualStartReadinessWait(ctx context.Context, window time.Duration) time.Duration {
	if window <= 0 {
		window = virtualStartReadinessBudget
	}
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < window {
				return max(0, remaining)
			}
		}
	}
	return window
}

// virtualStartBudgetExhausted reports whether the start phase ran out while the
// request itself is still alive. That is a server-side timing condition, not a
// verdict about the release: the caller answers the retryable 503 and stores
// nothing, so a retry resolves again instead of replaying a terminal this start
// never reached. requestCtx is the context captured before the phase narrowed
// the request.
func virtualStartBudgetExhausted(startCtx, requestCtx context.Context) bool {
	return startCtx != nil && requestCtx != nil &&
		errors.Is(startCtx.Err(), context.DeadlineExceeded) &&
		requestCtx.Err() == nil
}

// startBudgetExhaustedOperationErrorV3 is the answer for a start whose phase ran
// out: the retryable 503 with Retry-After, in the same words the failed
// terminal-save mapping uses for a deadline. The release was not indicted, so
// no verdict is published for it.
func startBudgetExhaustedOperationErrorV3() *PlaybackOperationError {
	return retryableStartOperationErrorV3(&transportErrorV3{cause: context.DeadlineExceeded})
}

// virtualPinRetryBudget reports how long the completed-release retry may run
// and whether it is worth attempting at all. A context without a deadline
// keeps the historical bounded retry. A deadline with less than
// virtualPinRetryMinBudget left skips the retry: the start is already at its
// end-to-end bound, so another re-list would only delay the terminal path.
func virtualPinRetryBudget(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return virtualCompletedReleaseRetryBudget, true
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return virtualCompletedReleaseRetryBudget, true
	}
	remaining := time.Until(deadline)
	if remaining < virtualPinRetryMinBudget {
		return remaining, false
	}
	if remaining > virtualCompletedReleaseRetryBudget {
		remaining = virtualCompletedReleaseRetryBudget
	}
	return remaining, true
}
