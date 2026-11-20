package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
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

	// Candidate resolve derives from the start deadline through its own resolve
	// half, which in turn wraps the cold path's virtualStartupBudget.
	resolve, resolveCancel := withVirtualStartPhaseDeadline(start, virtualStartResolveBudget)
	defer resolveCancel()
	resolveDeadline, _ := resolve.Deadline()
	if resolveDeadline.After(startDeadline) {
		t.Fatalf("resolve deadline %v outlives the start deadline %v", resolveDeadline, startDeadline)
	}

	// Transport startup and the pin retry derive from the phase, so neither can
	// hand a stage a fresh full budget.
	transport, transportCancel := context.WithTimeout(start, virtualStartupBudget)
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

// The phase is the sum of the work it holds, so no stage inside it is handed a
// bound its parent cannot reach. The 30 s phase this replaces was smaller than
// the 60 s cold path it contained and than the 95 s auto walk, and a resolve
// that spent most of it left the readiness wait a few seconds: a healthy
// hw_accel=auto start therefore terminalised as transcode_start_failed.
func TestVirtualStartPhaseBudgetCoversWhatItContains(t *testing.T) {
	// The phase is built from the stages, not chosen beside them.
	if want := virtualStartResolveBudget + max(virtualStartReadinessBudget, localAutoStartupBudgetV3); virtualStartPhaseBudget != want {
		t.Fatalf("phase = %v, want resolve + max(readiness, auto walk) = %v", virtualStartPhaseBudget, want)
	}

	// Both shapes the start can take: a fixed-executor transport waits one
	// ManifestStartupTimeout, an auto walk waits its three paths.
	if virtualStartPhaseBudget < virtualStartResolveBudget+virtualStartReadinessBudget {
		t.Fatalf("phase %v does not hold a resolve (%v) plus a readiness window (%v)",
			virtualStartPhaseBudget, virtualStartResolveBudget, virtualStartReadinessBudget)
	}
	if virtualStartPhaseBudget < virtualStartResolveBudget+localAutoStartupBudgetV3 {
		t.Fatalf("phase %v does not hold a resolve (%v) plus the auto walk (%v)",
			virtualStartPhaseBudget, virtualStartResolveBudget, localAutoStartupBudgetV3)
	}

	// The auto walk fits one readiness window instead of three, and does so by
	// bounding each path rather than the walk alone.
	if localAutoStartupBudgetV3 > virtualStartReadinessBudget {
		t.Fatalf("auto walk %v does not fit the readiness window %v", localAutoStartupBudgetV3, virtualStartReadinessBudget)
	}
	perPathTotal := time.Duration(playback.MaxAutoTranscodeStartupAttempts) * localAutoStartupPerPathV3
	if localAutoStartupBudgetV3 < perPathTotal {
		t.Fatalf("auto walk budget %v is smaller than its %d per-path allowances (%v)",
			localAutoStartupBudgetV3, playback.MaxAutoTranscodeStartupAttempts, perPathTotal)
	}
	if localAutoStartupPerPathV3 >= playback.ManifestStartupTimeout {
		t.Fatalf("auto per-path allowance %v is not shorter than one full manifest window (%v)",
			localAutoStartupPerPathV3, playback.ManifestStartupTimeout)
	}

	// Every child of the resolve fits inside it: the listing window plus the
	// probe it must leave room for, and the listing stage's own cap.
	if virtualStartResolveBudget != virtualStartListingBudget+virtualStartProbeBudget {
		t.Fatalf("resolve budget %v, want listing %v + probe %v",
			virtualStartResolveBudget, virtualStartListingBudget, virtualStartProbeBudget)
	}
	if virtualStartListingBudget > virtualListingStageBudget {
		t.Fatalf("start listing window %v exceeds the listing stage cap %v",
			virtualStartListingBudget, virtualListingStageBudget)
	}
	// The reserve is what makes a slow listing unable to pre-empt the probe; it
	// must not be capped below the probe's own bound in a start's resolve.
	if reserve := virtualStartProbeReserve(true, virtualStartResolveBudget); reserve < virtualStartProbeBudget {
		t.Fatalf("probe reserve %v is smaller than the probe budget %v in a start resolve",
			reserve, virtualStartProbeBudget)
	}

	// A phase longer than the client's own start abort is never delivered.
	if virtualStartPhaseBudget >= virtualStartClientBudget {
		t.Fatalf("phase %v does not leave a margin under the client's %v start abort",
			virtualStartPhaseBudget, virtualStartClientBudget)
	}
}

// A start whose resolve spends most of its allowance still hands the readiness
// wait a full window. The deadline is asserted directly rather than slept for:
// the phase is sized as resolve + readiness, so the resolve half may be spent
// entirely before the transport asks.
func TestVirtualStartReadinessKeepsItsWindowAfterASlowResolve(t *testing.T) {
	phaseStart := time.Now()
	spentResolveDeadline := phaseStart.Add(virtualStartResolveBudget)
	phaseDeadline := spentResolveDeadline.Add(virtualStartReadinessBudget)

	// A phase at the moment the resolve half has been spent entirely: the
	// deadline the transport sees is the phase's own, not the resolve's.
	phase, cancel := context.WithDeadline(context.Background(), phaseDeadline)
	defer cancel()
	if remaining := time.Until(phaseDeadline); remaining < virtualStartReadinessBudget-100*time.Millisecond {
		t.Fatalf("phase left %v after a spent resolve, want the %v window", remaining, virtualStartReadinessBudget)
	}

	got := virtualStartReadinessWait(phase, virtualStartReadinessBudget)
	if got < virtualStartReadinessBudget-100*time.Millisecond {
		t.Fatalf("readiness wait after a spent resolve = %v, want the full %v window", got, virtualStartReadinessBudget)
	}
	if got > virtualStartReadinessBudget {
		t.Fatalf("readiness wait = %v, want at most its own %v window", got, virtualStartReadinessBudget)
	}

	// The clamp still applies when the phase itself is nearly out: the wait
	// never claims time the phase does not have.
	shortPhase, shortCancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer shortCancel()
	if got := virtualStartReadinessWait(shortPhase, virtualStartReadinessBudget); got > time.Second {
		t.Fatalf("readiness wait = %v, want at most the second the phase has left", got)
	}

	// An exhausted phase is a zero wait, not a negative one.
	expired, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expiredCancel()
	if got := virtualStartReadinessWait(expired, 0); got != 0 {
		t.Fatalf("readiness wait on an expired phase = %v, want 0", got)
	}
	if got := virtualStartReadinessWait(context.Background(), 0); got != virtualStartReadinessBudget {
		t.Fatalf("readiness wait with no phase = %v, want the %v window", got, virtualStartReadinessBudget)
	}
}

// A phase that ran out while the request is still alive is the only state that
// answers "out of time". A request the client canceled, or a request that never
// had a phase, must not be reclassified.
func TestVirtualStartBudgetExhaustedRequiresALiveRequest(t *testing.T) {
	phaseExpired, phaseCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer phaseCancel()
	if !virtualStartBudgetExhausted(phaseExpired, context.Background()) {
		t.Fatal("an expired phase with a live request was not reported as exhausted")
	}
	if virtualStartBudgetExhausted(phaseExpired, phaseExpired) {
		t.Fatal("a request that is itself expired was reported as a live request")
	}
	if virtualStartBudgetExhausted(context.Background(), context.Background()) {
		t.Fatal("a start with no phase deadline was reported as exhausted")
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if virtualStartBudgetExhausted(canceled, context.Background()) {
		t.Fatal("a canceled phase was reported as an exhausted budget")
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
