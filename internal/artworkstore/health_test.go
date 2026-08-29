package artworkstore

import (
	"context"
	"errors"
	"testing"
)

func TestHealthTrackerDebouncesTransportTransitions(t *testing.T) {
	tracker := newHealthTracker(BackendLocal, HealthHealthy)
	tracker.observe(HealthUnavailable)
	if state, _ := tracker.current(); state != HealthHealthy {
		t.Fatalf("state after one failed verdict = %q, want healthy", state)
	}
	tracker.observe(HealthUnavailable)
	if state, _ := tracker.current(); state != HealthUnavailable {
		t.Fatalf("state after two failed verdicts = %q, want unavailable", state)
	}
	tracker.observe(HealthHealthy)
	if state, _ := tracker.current(); state != HealthUnavailable {
		t.Fatalf("state after one recovery verdict = %q, want unavailable", state)
	}
	tracker.observe(HealthHealthy)
	if state, _ := tracker.current(); state != HealthHealthy {
		t.Fatalf("state after two recovery verdicts = %q, want healthy", state)
	}
}

func TestRequestFailuresRequireAConfirmingProbe(t *testing.T) {
	handle := &Handle{Backend: BackendLocal, health: newHealthTracker(BackendLocal, HealthHealthy), probeSignal: make(chan struct{}, 1)}
	for range 20 {
		handle.ReportFailure(context.Canceled)
		handle.ReportFailure(context.DeadlineExceeded)
		handle.ReportFailure(ErrInvalidKey)
	}
	if state, _ := handle.Health(); state != HealthHealthy {
		t.Fatalf("aborted-request storm changed health to %q", state)
	}
	handle.ReportFailure(errors.New("spurious read error"))
	handle.ReportFailure(errors.New("second spurious read error"))
	if state, _ := handle.Health(); state != HealthHealthy {
		t.Fatalf("request evidence alone changed health to %q", state)
	}
	handle.reportProbeFailure(errors.New("probe confirmed outage"))
	if state, _ := handle.Health(); state != HealthUnavailable {
		t.Fatalf("confirmed outage left health at %q", state)
	}
}

func TestSuccessfulOperationClearsPendingFailureEvidence(t *testing.T) {
	handle := &Handle{Backend: BackendLocal, health: newHealthTracker(BackendLocal, HealthHealthy), probeSignal: make(chan struct{}, 1)}
	handle.ReportFailure(errors.New("transient"))
	handle.reportSuccess()
	handle.reportProbeFailure(errors.New("unrelated later probe"))
	if state, _ := handle.Health(); state != HealthHealthy {
		t.Fatalf("stale request evidence survived success: %q", state)
	}
}

func TestRuntimeFailureSignalsImmediateProbeAndInventoryOwnsDegradedState(t *testing.T) {
	handle := &Handle{
		Backend:     BackendLocal,
		health:      newHealthTracker(BackendLocal, HealthHealthy),
		probeSignal: make(chan struct{}, 1),
	}
	handle.ReportFailure(errors.New("mount unavailable"))
	select {
	case <-handle.probeSignal:
	default:
		t.Fatal("first runtime transport failure did not signal an immediate probe")
	}
	handle.ReportInventoryMissing(2)
	if state, _ := handle.Health(); state != HealthDegraded {
		t.Fatalf("state with missing revisions = %q, want degraded", state)
	}
	handle.ReportInventoryMissing(0)
	if state, _ := handle.Health(); state != HealthHealthy {
		t.Fatalf("state after durable inventory recovered = %q, want healthy", state)
	}
	handle.health.force(HealthWrongMount)
	handle.ReportInventoryMissing(0)
	if state, _ := handle.Health(); state != HealthWrongMount {
		t.Fatalf("inventory recovery overrode wrong_mount with %q", state)
	}
}
