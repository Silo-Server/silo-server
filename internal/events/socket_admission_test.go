package events

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func newTestClientSocketAdmission(t *testing.T, perAccount, perProcess int, mode ClientSocketMode) *ClientSocketAdmission {
	t.Helper()
	a, err := NewClientSocketAdmission(perAccount, perProcess, mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.BeginDrain()
		if got := a.Occupancy(); got != 0 {
			t.Errorf("leaked reservations = %d", got)
		}
	})
	return a
}

func reserveTestClientSocket(t *testing.T, a *ClientSocketAdmission, accountID int) *ClientSocketReservation {
	t.Helper()
	r, reason := a.Reserve(accountID, false)
	if r == nil || reason != "" {
		t.Fatalf("reservation = %v, reason = %q; want admitted", r != nil, reason)
	}
	t.Cleanup(r.Release)
	return r
}

func assertClientSocketRefusal(t *testing.T, a *ClientSocketAdmission, accountID int, draining bool, want ClientSocketRefusal) {
	t.Helper()
	before := a.Occupancy()
	r, reason := a.Reserve(accountID, draining)
	if r != nil {
		r.Release()
		t.Fatal("refused reservation returned a handle")
	}
	if reason != want {
		t.Fatalf("refusal = %q, want %q", reason, want)
	}
	if got := a.Occupancy(); got != before {
		t.Fatalf("occupancy after refusal = %d, want %d", got, before)
	}
}

func assertClientSocketDone(t *testing.T, a *ClientSocketAdmission, wantClosed bool) {
	t.Helper()
	select {
	case <-a.Done():
		if !wantClosed {
			t.Fatal("drain completed too early")
		}
	default:
		if wantClosed {
			t.Fatal("drain did not complete")
		}
	}
}

func TestNewClientSocketAdmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		perAccount int
		perProcess int
		mode       ClientSocketMode
	}{
		{"zero account", 0, 1, ClientSocketObserve},
		{"negative account", -1, 1, ClientSocketEnforce},
		{"zero process", 1, 0, ClientSocketObserve},
		{"negative process", 1, -1, ClientSocketEnforce},
		{"both zero", 0, 0, ClientSocketEnforce},
		{"empty mode", 1, 1, ""},
		{"unknown mode", 1, 1, "other"},
		{"noncanonical mode", 1, 1, "Observe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewClientSocketAdmission(tc.perAccount, tc.perProcess, tc.mode)
			if err == nil || a != nil {
				t.Fatal("invalid constructor arguments accepted")
			}
		})
	}
	for _, mode := range []ClientSocketMode{ClientSocketObserve, ClientSocketEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			a := newTestClientSocketAdmission(t, 2, 1, mode)
			if a.Done() == nil || a.Done() != a.Done() || a.Occupancy() != 0 {
				t.Fatal("new admission must be empty with a stable drain channel")
			}
			assertClientSocketDone(t, a, false)
		})
	}
}

func TestClientSocketAdmissionPendingAccountBucketsAndRelease(t *testing.T) {
	a := newTestClientSocketAdmission(t, 2, 3, ClientSocketEnforce)
	// There is no activation step: pending reservations already spend the same
	// account bucket. Mapping actual profiles/credentials to that ID belongs
	// to handler integration tests, not to this account-only core API.
	first := reserveTestClientSocket(t, a, 7)
	second := reserveTestClientSocket(t, a, 7)
	assertClientSocketRefusal(t, a, 7, false, ClientSocketAccountLimit)
	other := reserveTestClientSocket(t, a, 8)
	assertClientSocketRefusal(t, a, 9, false, ClientSocketProcessLimit)
	if got := a.Occupancy(); got != 3 {
		t.Fatalf("pending occupancy = %d, want 3", got)
	}
	first.Release()
	first.Release()
	if got := a.Occupancy(); got != 2 {
		t.Fatalf("occupancy after duplicate release = %d, want 2", got)
	}
	replacement := reserveTestClientSocket(t, a, 7)
	first.Release() // An old handle cannot release its replacement's capacity.
	if got := a.Occupancy(); got != 3 {
		t.Fatalf("occupancy after replacing released reservation = %d, want 3", got)
	}
	second.Release()
	other.Release()
	replacement.Release()
	a.mu.Lock()
	remainingAccounts := len(a.accounts)
	a.mu.Unlock()
	if a.Occupancy() != 0 || remainingAccounts != 0 {
		t.Fatal("released capacity or empty account buckets were retained")
	}
	assertClientSocketDone(t, a, false)
}

func TestClientSocketAdmissionProcessRefusalTakesPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		perAccount int
	}{
		{"both full", 1},
		{"process cap below account cap", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestClientSocketAdmission(t, tc.perAccount, 1, ClientSocketEnforce)
			reserveTestClientSocket(t, a, 7)
			// Even when both are full, aggregate capacity must map to 503.
			assertClientSocketRefusal(t, a, 7, false, ClientSocketProcessLimit)
			assertClientSocketRefusal(t, a, 8, false, ClientSocketProcessLimit)
		})
	}
}

func TestClientSocketAdmissionFailedWorkReleasesPendingCapacity(t *testing.T) {
	a := newTestClientSocketAdmission(t, 1, 1, ClientSocketEnforce)
	failedWork := errors.New("simulated work failure")
	err := func() error {
		r := reserveTestClientSocket(t, a, 7)
		defer r.Release()
		return failedWork
	}()
	if !errors.Is(err, failedWork) || a.Occupancy() != 0 {
		t.Fatal("failed work did not release pending capacity")
	}
	reserveTestClientSocket(t, a, 7)
}

func TestClientSocketAdmissionDraining(t *testing.T) {
	for _, mode := range []ClientSocketMode{ClientSocketObserve, ClientSocketEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			a := newTestClientSocketAdmission(t, 1, 1, mode)
			assertClientSocketRefusal(t, a, 7, true, ClientSocketDraining)
			assertClientSocketDone(t, a, false)
			r := reserveTestClientSocket(t, a, 7)
			r.Release()
			assertClientSocketDone(t, a, false)
			a.BeginDrain()
			assertClientSocketDone(t, a, true)
			a.BeginDrain()
			assertClientSocketRefusal(t, a, 7, false, ClientSocketDraining)
			assertClientSocketRefusal(t, a, 7, true, ClientSocketDraining)
		})
	}
}

func TestClientSocketAdmissionDrainWaitsForActiveAndCanceledPendingWork(t *testing.T) {
	a := newTestClientSocketAdmission(t, 2, 3, ClientSocketEnforce)
	active := reserveTestClientSocket(t, a, 7)
	pending := reserveTestClientSocket(t, a, 8)
	done := a.Done()
	a.BeginDrain()
	a.BeginDrain()
	assertClientSocketDone(t, a, false)
	assertClientSocketRefusal(t, a, 9, false, ClientSocketDraining)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := func() error {
		defer pending.Release()
		return ctx.Err()
	}()
	if !errors.Is(err, context.Canceled) || a.Occupancy() != 1 {
		t.Fatal("canceled pending work did not release exactly one reservation")
	}
	assertClientSocketDone(t, a, false)
	active.Release()
	assertClientSocketDone(t, a, true)
	if a.Done() != done {
		t.Fatal("drain channel changed")
	}
	pending.Release()
	active.Release()
	a.BeginDrain()
	if got := a.Occupancy(); got != 0 {
		t.Fatalf("occupancy after drain = %d, want 0", got)
	}
}

func TestClientSocketAdmissionProcessesAreIndependent(t *testing.T) {
	old := newTestClientSocketAdmission(t, 1, 1, ClientSocketEnforce)
	oldReservation := reserveTestClientSocket(t, old, 7)
	replacement := newTestClientSocketAdmission(t, 1, 1, ClientSocketEnforce)
	if replacement.Occupancy() != 0 {
		t.Fatal("replacement process inherited ghost reservations")
	}
	reserveTestClientSocket(t, replacement, 7)
	old.BeginDrain()
	oldReservation.Release()
	assertClientSocketDone(t, old, true)
	assertClientSocketDone(t, replacement, false)
	if got := replacement.Occupancy(); got != 1 {
		t.Fatalf("replacement occupancy = %d, want 1", got)
	}
}

func TestClientSocketAdmissionUnavailable(t *testing.T) {
	var missing *ClientSocketAdmission
	assertClientSocketRefusal(t, missing, 7, false, ClientSocketUnavailable)
	assertClientSocketRefusal(t, missing, 7, true, ClientSocketUnavailable)
	missing.BeginDrain()
	if missing.Done() != nil {
		t.Fatal("missing admission returned a drain channel")
	}
	var zero ClientSocketAdmission
	assertClientSocketRefusal(t, &zero, 7, false, ClientSocketUnavailable)
	zero.BeginDrain()
	if zero.Done() != nil {
		t.Fatal("uninitialized admission returned a drain channel")
	}
	for _, mode := range []ClientSocketMode{ClientSocketObserve, ClientSocketEnforce} {
		a := newTestClientSocketAdmission(t, 1, 1, mode)
		assertClientSocketRefusal(t, a, 0, false, ClientSocketUnavailable)
		assertClientSocketRefusal(t, a, -1, false, ClientSocketUnavailable)
		reserveTestClientSocket(t, a, 7)
		assertClientSocketRefusal(t, a, 0, false, ClientSocketUnavailable)
	}
	var noReservation *ClientSocketReservation
	noReservation.Release()
	new(ClientSocketReservation).Release()
}

func TestClientSocketAdmissionAtomicReservation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		perAccount int
		perProcess int
		accounts   [3]int
		refusal    ClientSocketRefusal
	}{
		{"account", 1, 3, [3]int{7, 7, 7}, ClientSocketAccountLimit},
		{"process", 3, 1, [3]int{7, 8, 9}, ClientSocketProcessLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestClientSocketAdmission(t, tc.perAccount, tc.perProcess, ClientSocketEnforce)
			type result struct {
				reservation *ClientSocketReservation
				reason      ClientSocketRefusal
			}
			// Exactly three attempts, held until every goroutine has joined.
			results := make(chan result, 3)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for _, accountID := range tc.accounts {
				wg.Go(func() {
					<-start
					r, reason := a.Reserve(accountID, false)
					results <- result{r, reason}
				})
			}
			close(start)
			wg.Wait()
			close(results)
			admitted, refused := 0, 0
			for result := range results {
				if result.reservation != nil {
					t.Cleanup(result.reservation.Release)
					admitted++
					if result.reason != "" {
						t.Error("admitted reservation has a refusal reason")
					}
				} else if result.reason == tc.refusal {
					refused++
				} else {
					t.Errorf("refusal = %q, want %q", result.reason, tc.refusal)
				}
			}
			if admitted != 1 || refused != 2 || a.Occupancy() != 1 {
				t.Fatalf("admitted = %d, refused = %d, occupancy = %d; want 1, 2, 1", admitted, refused, a.Occupancy())
			}
		})
	}
}

func TestClientSocketReservationConcurrentRelease(t *testing.T) {
	a := newTestClientSocketAdmission(t, 2, 2, ClientSocketEnforce)
	first := reserveTestClientSocket(t, a, 7)
	second := reserveTestClientSocket(t, a, 7)
	a.BeginDrain()
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(first.Release)
	}
	wg.Wait()
	if got := a.Occupancy(); got != 1 {
		t.Fatalf("occupancy after simultaneous duplicate release = %d, want 1", got)
	}
	assertClientSocketDone(t, a, false)
	second.Release()
	assertClientSocketDone(t, a, true)
}

func TestClientSocketAdmissionConcurrentDrain(t *testing.T) {
	a := newTestClientSocketAdmission(t, 2, 2, ClientSocketEnforce)
	first := reserveTestClientSocket(t, a, 7)
	var pending *ClientSocketReservation
	var refusal ClientSocketRefusal
	start := make(chan struct{})
	var wg sync.WaitGroup
	// Three finite operations check the last-release/drain/reserve interleaving.
	wg.Go(func() {
		<-start
		first.Release()
	})
	wg.Go(func() {
		<-start
		a.BeginDrain()
	})
	wg.Go(func() {
		<-start
		pending, refusal = a.Reserve(8, false)
	})
	close(start)
	wg.Wait()
	if pending != nil {
		t.Cleanup(pending.Release)
		if refusal != "" {
			t.Error("reservation preceding drain has a refusal reason")
		}
		assertClientSocketDone(t, a, false)
		pending.Release()
	} else if refusal != ClientSocketDraining {
		t.Errorf("reservation after drain = %q, want draining", refusal)
	}
	assertClientSocketDone(t, a, true)
	if got := a.Occupancy(); got != 0 {
		t.Fatalf("occupancy after concurrent drain = %d, want 0", got)
	}
}
