package handlers

import (
	"context"
	"time"
)

// virtualStartPhaseBudget bounds one user-visible virtual start: candidate
// listing/resolve, transport startup, and the completed-release pin retry all
// observe the same deadline. Provider listings measured 17–25 s in production,
// so 30 s admits the slow band with margin while keeping the catalog discovery
// budget (20 s, detached from the start request) and the pin retry (20 s) from
// stacking into a 40 s-plus start. The budget is a var so tests can shrink it.
var virtualStartPhaseBudget = 30 * time.Second

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
