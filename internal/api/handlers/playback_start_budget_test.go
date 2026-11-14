package handlers

import (
	"context"
	"testing"
	"time"
)

// The catalog discovery budget and the completed-release retry budget are both
// 20 s. One start must not be able to spend both in full: every stage derives
// from a single start-phase deadline, so the retry only sees what is left and
// is skipped entirely once too little remains.
func TestVirtualStartPhaseBudgetBoundsCombinedStart(t *testing.T) {
	start, cancel := withVirtualStartPhaseDeadline(context.Background(), virtualStartPhaseBudget)
	defer cancel()
	startDeadline, ok := start.Deadline()
	if !ok {
		t.Fatal("start phase has no deadline")
	}

	// Candidate resolve derives from the start deadline (the real path wraps
	// r.Context() with virtualStartupBudget).
	resolve, resolveCancel := context.WithTimeout(start, virtualStartupBudget)
	defer resolveCancel()
	resolveDeadline, _ := resolve.Deadline()
	if resolveDeadline.After(startDeadline) {
		t.Fatalf("resolve deadline %v outlives the start deadline %v", resolveDeadline, startDeadline)
	}

	// Transport startup and the pin retry derive from the same resolve
	// context, so neither can hand a stage a fresh full budget.
	transport, transportCancel := context.WithTimeout(resolve, virtualStartupBudget)
	defer transportCancel()
	transportDeadline, _ := transport.Deadline()
	if transportDeadline.After(startDeadline) {
		t.Fatalf("transport deadline %v outlives the start deadline %v", transportDeadline, startDeadline)
	}
	if _, allowed := virtualPinRetryBudget(transport); !allowed {
		t.Fatal("the pin retry was skipped before any budget was spent")
	}
	if budget, allowed := virtualPinRetryBudget(transport); !allowed || budget > virtualCompletedReleaseRetryBudget {
		t.Fatalf("pin retry budget = %v allowed=%v, want a bounded retry", budget, allowed)
	}
}

func TestVirtualPinRetrySkippedWhenStartBudgetExhausted(t *testing.T) {
	// A catalog resolve that consumed the start leaves less than the minimum
	// retry budget; the retry must be skipped rather than open another
	// provider round-trip the deadline would cut off.
	spent, cancel := context.WithDeadline(context.Background(), time.Now().Add(virtualPinRetryMinBudget/2))
	defer cancel()
	if budget, allowed := virtualPinRetryBudget(spent); allowed {
		t.Fatalf("pin retry allowed with %v left, want it skipped below %v", budget, virtualPinRetryMinBudget)
	}

	// A stage with more than the minimum but less than the historical retry
	// budget is shortened to what remains.
	partial, partialCancel := context.WithDeadline(context.Background(), time.Now().Add(virtualPinRetryMinBudget+time.Second))
	defer partialCancel()
	budget, allowed := virtualPinRetryBudget(partial)
	if !allowed {
		t.Fatal("pin retry should run with more than the minimum remaining")
	}
	if budget > virtualPinRetryMinBudget+time.Second || budget <= virtualPinRetryMinBudget {
		t.Fatalf("pin retry budget = %v, want the remaining start budget", budget)
	}
}

// A virtual resolve attempt takes its bound from the request's remaining time
// under an explicit cap, and the answer is never a negative duration. A derived
// context built with a bare WithTimeout on an already-consumed deadline used to
// report bound_ms=-34/-65/-95/-126 and pre-empt every wait inside it before the
// wait started, which the version fallback then read as a provider listing
// failure. The helper refuses the attempt instead, so the caller answers a
// truthful retryable terminal.
func TestVirtualResolveAttemptBudgetIsPositiveAndRequestDerived(t *testing.T) {
	// No deadline: the cap is the whole budget.
	if budget, ok := virtualResolveAttemptBudget(context.Background(), virtualStartupBudget); !ok || budget != virtualStartupBudget {
		t.Fatalf("budget with no deadline = %v ok=%v, want the %v cap", budget, ok, virtualStartupBudget)
	}

	// The request's remaining time wins when it is smaller than the cap.
	requestCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	budget, ok := virtualResolveAttemptBudget(requestCtx, virtualStartupBudget)
	if !ok {
		t.Fatal("a request with 3s left was refused")
	}
	if budget <= 0 {
		t.Fatalf("budget = %v, want a positive duration", budget)
	}
	if budget > 3*time.Second {
		t.Fatalf("budget = %v, want at most the 3s the request has left", budget)
	}

	// A short cap wins when the request has more time than the attempt needs.
	capped, ok := virtualResolveAttemptBudget(requestCtx, time.Second)
	if !ok || capped != time.Second {
		t.Fatalf("budget = %v ok=%v, want the 1s cap", capped, ok)
	}

	// An already-spent deadline is a refusal, never a negative bound.
	spent, spentCancel := context.WithDeadline(context.Background(), time.Now().Add(-40*time.Millisecond))
	defer spentCancel()
	budget, ok = virtualResolveAttemptBudget(spent, virtualStartupBudget)
	if ok {
		t.Fatalf("budget with an expired deadline = %v, want the attempt refused", budget)
	}
	if budget != 0 {
		t.Fatalf("refused budget = %v, want 0 rather than a negative duration", budget)
	}
	if got := virtualResolveRemainingMilliseconds(spent); got != 0 {
		t.Fatalf("remaining ms = %d, want 0 for an expired deadline", got)
	}

	// The log helper never reports time left for a deadline that already passed.
	expired, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-90*time.Millisecond))
	defer expiredCancel()
	if got := virtualResolveRemainingMilliseconds(expired); got != 0 {
		t.Fatalf("remaining ms = %d, want 0 for an expired deadline", got)
	}
}
