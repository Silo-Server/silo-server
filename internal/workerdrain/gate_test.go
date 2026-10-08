package workerdrain

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainGatePreservesAdmittedWorkAndSealsLateAdmission(t *testing.T) {
	var fence atomic.Value
	fence.Store("")
	var busy atomic.Int64
	var denied atomic.Bool
	source := func(context.Context) (int, string, string, error) {
		return 7, fence.Load().(string), "synthetic-realm", nil
	}
	gate := New(source, func() int { return int(busy.Load()) }, func(context.Context, string) bool { return denied.Load() })
	expiry := time.Now().Add(time.Hour)
	end, err := gate.Begin(t.Context(), "existing", expiry)
	if err != nil {
		t.Fatal(err)
	}
	fence.Store("retirement-1")
	status, err := gate.Observe(t.Context())
	if err != nil || !status.Fenced || status.Drained || status.ActiveRequests != 1 || status.ActiveReservations != 1 {
		t.Fatalf("in-flight admission lost: %+v %v", status, err)
	}
	if _, err = gate.Begin(t.Context(), "late-dispatch", expiry); !errors.Is(err, ErrFenced) {
		t.Fatalf("late admission: %v", err)
	}
	continued, err := gate.Begin(t.Context(), "existing", expiry)
	if err != nil {
		t.Fatalf("existing stream refused: %v", err)
	}
	continued()
	end()
	end()
	busy.Store(1)
	denied.Store(true)
	status, err = gate.Observe(t.Context())
	if err != nil || status.Drained || status.ActiveJobs != 1 || status.ActiveRequests != 0 || status.ActiveReservations != 0 {
		t.Fatalf("teardown execution lost: %+v %v", status, err)
	}
	busy.Store(0)
	status, err = gate.Observe(t.Context())
	if err != nil || !status.Drained || status.WorkerInstanceID == "" || status.ObservedAt.IsZero() {
		t.Fatalf("zero proof: %+v %v", status, err)
	}
	for _, key := range []string{"existing", "late-dispatch", ""} {
		if _, err = gate.Begin(t.Context(), key, expiry); !errors.Is(err, ErrFenced) {
			t.Fatalf("sealed worker accepted %q: %v", key, err)
		}
	}
	// Explicit durable cancellation is the only path that restores admission.
	fence.Store("")
	reopened, err := gate.Begin(t.Context(), "new", expiry)
	if err != nil {
		t.Fatal(err)
	}
	reopened()
	status, err = gate.Observe(t.Context())
	if err != nil || status.Fenced || status.Drained {
		t.Fatalf("cancellation: %+v %v", status, err)
	}
}

func TestDrainGateRestartAndUncertainAuthority(t *testing.T) {
	var unavailable atomic.Bool
	source := func(context.Context) (int, string, string, error) {
		if unavailable.Load() {
			return 0, "", "", ErrUnavailable
		}
		return 7, "durable-fence", "synthetic-realm", nil
	}
	first := New(source, nil, nil)
	before, err := first.Observe(t.Context())
	if err != nil || !before.Drained {
		t.Fatalf("initial zero: %+v %v", before, err)
	}
	restarted := New(source, nil, nil)
	after, err := restarted.Observe(t.Context())
	if err != nil || !after.Drained || after.WorkerInstanceID == before.WorkerInstanceID {
		t.Fatalf("restart fence: %+v %v", after, err)
	}
	if _, err = restarted.Begin(t.Context(), "old-token", time.Now().Add(time.Hour)); !errors.Is(err, ErrFenced) {
		t.Fatalf("resurrection: %v", err)
	}
	unavailable.Store(true)
	if _, err = restarted.Observe(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable source became proof: %v", err)
	}
	if _, err = restarted.Begin(t.Context(), "anything", time.Now().Add(time.Hour)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable source admitted: %v", err)
	}
}

func TestDrainGatePermitExpiryIsConservative(t *testing.T) {
	var fence string
	gate := New(func(context.Context) (int, string, string, error) { return 7, fence, "synthetic-realm", nil }, nil, nil)
	now := time.Now()
	gate.now = func() time.Time { return now }
	end, err := gate.Begin(t.Context(), "token", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	end()
	fence = "retirement"
	status, err := gate.Observe(t.Context())
	if err != nil || status.Drained || status.ActiveReservations != 1 {
		t.Fatalf("quiet token incorrectly retired: %+v %v", status, err)
	}
	now = now.Add(time.Hour)
	status, err = gate.Observe(t.Context())
	if err != nil || !status.Drained {
		t.Fatalf("expired permit: %+v %v", status, err)
	}
}

func TestDrainGateAuthorityOutagePreservesOnlyBoundedExistingPlayback(t *testing.T) {
	var unavailable bool
	gate := New(func(context.Context) (int, string, string, error) {
		if unavailable {
			return 0, "", "", ErrUnavailable
		}
		return 7, "", "synthetic-realm", nil
	}, nil, nil)
	now := time.Now()
	gate.now = func() time.Time { return now }
	end, err := gate.Begin(t.Context(), "existing", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	end()
	unavailable = true
	continued, err := gate.Begin(t.Context(), "existing", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("authority outage interrupted admitted playback: %v", err)
	}
	continued()
	if _, err = gate.Observe(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("outage yielded retirement proof: %v", err)
	}
	for _, key := range []string{"new", ""} {
		if _, err = gate.Begin(t.Context(), key, now.Add(time.Hour)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("outage admitted %q: %v", key, err)
		}
	}
	now = now.Add(time.Minute)
	if _, err = gate.Begin(t.Context(), "existing", now.Add(time.Hour)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("outage extended original permit: %v", err)
	}
}

func TestDrainGateRejectsMissingAndChangedRealm(t *testing.T) {
	for _, initial := range []string{"", "synthetic-realm"} {
		t.Run(initial, func(t *testing.T) {
			realm := initial
			gate := New(func(context.Context) (int, string, string, error) { return 7, "durable-fence", realm, nil }, nil, nil)
			before, err := gate.Observe(t.Context())
			if realm == "" {
				if !errors.Is(err, ErrUnavailable) || before.Drained || gate.PublicIdentity(t.Context()).NativeServerID != "" {
					t.Fatalf("missing realm accepted: %+v %v", before, err)
				}
				return
			}
			if err != nil || !before.Drained || before.NativeServerID != realm {
				t.Fatalf("bound realm rejected: %+v %v", before, err)
			}
			realm = "foreign-realm"
			if after, err := gate.Observe(t.Context()); !errors.Is(err, ErrUnavailable) || after.Drained {
				t.Fatalf("changed realm accepted: %+v %v", after, err)
			}
		})
	}
}
