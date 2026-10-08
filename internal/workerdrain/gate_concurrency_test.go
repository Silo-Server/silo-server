package workerdrain

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainGateAuthorityLookupDoesNotBlockStateOrOtherLookups(t *testing.T) {
	for _, operation := range []string{"release", "admission", "observation"} {
		t.Run(operation, func(t *testing.T) {
			var blocked atomic.Bool
			entered := make(chan struct{}, 4)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			gate := New(func(ctx context.Context) (int, string, string, error) {
				if blocked.Load() {
					entered <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return 0, "", "", ctx.Err()
					}
				}
				return 7, "", "synthetic-realm", nil
			}, nil, nil)
			expiry := time.Now().Add(time.Hour)
			end, err := gate.Begin(t.Context(), "existing", expiry)
			if err != nil {
				t.Fatal(err)
			}
			blocked.Store(true)
			firstDone := make(chan error, 1)
			go func() {
				finish, err := gate.Begin(t.Context(), "existing", expiry)
				if err == nil {
					finish()
				}
				firstDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first lookup never entered")
			}
			secondDone := make(chan error, 1)
			defer func() {
				unblock()
				end()
				if err := <-firstDone; err != nil {
					t.Errorf("first lookup cleanup: %v", err)
				}
				if err := <-secondDone; err != nil {
					t.Errorf("second lookup cleanup: %v", err)
				}
				if proof, err := gate.Observe(t.Context()); err != nil || proof.ActiveRequests != 0 {
					t.Errorf("request cleanup: %+v %v", proof, err)
				}
			}()
			switch operation {
			case "release":
				go func() { end(); secondDone <- nil }()
				select {
				case err := <-secondDone:
					secondDone <- err
				case <-time.After(3 * time.Second):
					t.Fatal("authority I/O blocked request release")
				}
			case "admission":
				go func() {
					finish, err := gate.Begin(t.Context(), "another", expiry)
					if err == nil {
						finish()
					}
					secondDone <- err
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("authority I/O serialized independent admission")
				}
			case "observation":
				go func() { _, err := gate.Observe(t.Context()); secondDone <- err }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("authority I/O blocked fresh drain observation")
				}
			}
		})
	}
}

func TestDrainGateLateAuthoritySnapshotCannotReopenSealedAdmission(t *testing.T) {
	var fence atomic.Value
	fence.Store("")
	var delay atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	gate := New(func(ctx context.Context) (int, string, string, error) {
		current := fence.Load().(string)
		if delay.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return 0, "", "", ctx.Err()
			}
		}
		return 7, current, "synthetic-realm", nil
	}, nil, nil)
	gate.PublicIdentity(t.Context())
	delay.Store(true)
	lateDone := make(chan error, 1)
	go func() {
		finish, err := gate.Begin(t.Context(), "late", time.Now().Add(time.Hour))
		if err == nil {
			finish()
		}
		lateDone <- err
	}()
	defer func() { unblock(); <-lateDone }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old unfenced source snapshot not captured")
	}
	fence.Store("durable-fence")
	proofDone := make(chan struct{})
	var proof Status
	var proofErr error
	go func() { proof, proofErr = gate.Observe(t.Context()); close(proofDone) }()
	select {
	case <-proofDone:
	case <-time.After(3 * time.Second):
		unblock()
		<-proofDone
		t.Fatal("old snapshot blocked sealed observation")
	}
	if proofErr != nil || !proof.Drained {
		t.Fatalf("fresh zero proof: %+v %v", proof, proofErr)
	}
	unblock()
	err := <-lateDone
	lateDone <- err
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("stale unfenced snapshot admitted after sealed proof: %v", err)
	}
	after, err := gate.Observe(t.Context())
	if err != nil || !after.Drained || after.ActiveRequests != 0 || after.ActiveReservations != 0 {
		t.Fatalf("sealed receipt invalidated: %+v %v", after, err)
	}
}

func TestDrainGatePositiveIdentityLossRefusesExistingUnsealedPermit(t *testing.T) {
	for _, variant := range []string{"different node", "different realm", "missing node", "missing realm"} {
		t.Run(variant, func(t *testing.T) {
			id, realm := 7, "synthetic-realm"
			gate := New(func(context.Context) (int, string, string, error) { return id, "", realm, nil }, nil, nil)
			end, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			end()
			switch variant {
			case "different node":
				id = 8
			case "different realm":
				realm = "foreign-realm"
			case "missing node":
				id = 0
			case "missing realm":
				realm = ""
			}
			finish, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
			if finish != nil {
				finish()
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("positive identity loss allowed existing playback: %v", err)
			}
		})
	}
}

func TestDrainGateRepeatedIdentityLossInvalidatesDelayedRestoration(t *testing.T) {
	var foreign atomic.Bool
	var delay atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	gate := New(func(context.Context) (int, string, string, error) {
		realm := "synthetic-realm"
		if foreign.Load() {
			realm = "foreign-realm"
		}
		if delay.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return 7, "", realm, nil
	}, nil, nil)
	end, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	end()
	foreign.Store(true)
	if _, err := gate.Observe(t.Context()); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatal(err)
	}
	foreign.Store(false)
	delay.Store(true)
	done := make(chan error, 1)
	go func() {
		finish, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
		if finish != nil {
			finish()
		}
		done <- err
	}()
	defer func() { unblock(); <-done }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("restoration did not enter source")
	}
	foreign.Store(true)
	if _, err := gate.Observe(t.Context()); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatal(err)
	}
	unblock()
	err = <-done
	done <- err
	if !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatalf("stale restoration reopened foreign identity: %v", err)
	}
}

func TestDrainGateStaleAuthorityErrorCannotUseOutageContinuity(t *testing.T) {
	var foreign atomic.Bool
	var delay atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	gate := New(func(context.Context) (int, string, string, error) {
		if delay.CompareAndSwap(true, false) {
			close(entered)
			<-release
			return 0, "", "", ErrUnavailable
		}
		if foreign.Load() {
			return 7, "", "foreign-realm", nil
		}
		return 7, "", "synthetic-realm", nil
	}, nil, nil)
	end, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	end()
	delay.Store(true)
	done := make(chan error, 1)
	go func() {
		finish, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
		if finish != nil {
			finish()
		}
		done <- err
	}()
	defer func() { unblock(); <-done }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old error did not enter source")
	}
	foreign.Store(true)
	if _, err := gate.Observe(t.Context()); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatal(err)
	}
	unblock()
	err = <-done
	done <- err
	if !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatalf("old outage admitted after identity loss: %v", err)
	}
}

func TestDrainGateRepeatedStaleAuthorityRemainsClosed(t *testing.T) {
	gate := New(nil, nil, nil)
	gate.source = func(context.Context) (int, string, string, error) {
		// A newer positive authority observation wins each time this read runs.
		gate.mu.Lock()
		gate.authorityEpoch++
		gate.mu.Unlock()
		return 7, "", "synthetic-realm", nil
	}
	gate.mu.Lock()
	gate.retainLocked("existing", time.Now().Add(time.Hour))
	gate.mu.Unlock()
	finish, err := gate.Begin(t.Context(), "existing", time.Now().Add(time.Hour))
	if finish != nil {
		finish()
	}
	if !errors.Is(err, errAuthorityStale) {
		t.Fatalf("stale reads used bounded permit fallback: %v", err)
	}
}

func TestDrainGateRevocationLookupDoesNotBlockOrRevokeNewGeneration(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var deny atomic.Bool
	gate := New(func(context.Context) (int, string, string, error) { return 7, "", "synthetic-realm", nil }, nil, func(context.Context, string) bool {
		if deny.CompareAndSwap(true, false) {
			close(entered)
			<-release
			return true
		}
		return false
	})
	expiry := time.Now().Add(time.Hour)
	end, err := gate.Begin(t.Context(), "existing", expiry)
	if err != nil {
		t.Fatal(err)
	}
	deny.Store(true)
	done := make(chan error, 1)
	var status Status
	go func() { var err error; status, err = gate.Observe(t.Context()); done <- err }()
	defer func() { unblock(); end(); <-done }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("deny lookup not entered")
	}
	released := make(chan error, 1)
	go func() {
		end()
		gate.Revoke("existing")
		renewed, err := gate.Begin(t.Context(), "existing", expiry)
		if renewed != nil {
			renewed()
		}
		released <- err
	}()
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		unblock()
		<-released
		t.Fatal("remote deny lookup blocked release/revoke/admission")
	}
	unblock()
	err = <-done
	done <- err
	if err != nil || status.ActiveRequests != 0 || status.ActiveReservations != 1 {
		t.Fatalf("old positive deny revoked new generation: %+v %v", status, err)
	}
}

func TestDrainGatePreparationPublishesBeforeReservationRelease(t *testing.T) {
	var fence atomic.Value
	fence.Store("")
	gate := New(func(context.Context) (int, string, string, error) {
		return 7, fence.Load().(string), "synthetic-realm", nil
	}, nil, nil)
	finish, err := gate.BeginPreparation(t.Context(), "artifact:prepared", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fence.Store("durable-fence")
	before, err := gate.Observe(t.Context())
	if err != nil || before.Drained || before.ActiveRequests != 1 {
		t.Fatalf("preparation reservation: %+v %v", before, err)
	}
	finish(true)
	after, err := gate.Observe(t.Context())
	if err != nil || after.Drained || after.ActiveRequests != 0 || after.ActiveReservations != 1 {
		t.Fatalf("publication handoff: %+v %v", after, err)
	}
	if _, err := gate.BeginPreparation(t.Context(), "artifact:prepared", time.Now().Add(time.Hour)); !errors.Is(err, ErrFenced) {
		t.Fatalf("known preparation passed fence: %v", err)
	}
	gate.Revoke("artifact:prepared")
	after, err = gate.Observe(t.Context())
	if err != nil || !after.Drained {
		t.Fatalf("published artifact cleanup: %+v %v", after, err)
	}
}

func TestDrainGateColdRegistrationBindsOnlyFreshAuthority(t *testing.T) {
	var registered atomic.Bool
	gate := New(func(context.Context) (int, string, string, error) {
		if !registered.Load() {
			return 0, "", "", ErrIdentityUnavailable
		}
		return 7, "", "synthetic-realm", nil
	}, nil, nil)
	if identity := gate.PublicIdentity(t.Context()); identity.NativeServerID != "" || identity.WorkerInstanceID != "" {
		t.Fatalf("cold unregistered identity: %+v", identity)
	}
	if _, err := gate.Begin(t.Context(), "early", time.Now().Add(time.Hour)); !errors.Is(err, ErrIdentityUnavailable) {
		t.Fatalf("cold worker admitted before registration: %v", err)
	}
	registered.Store(true)
	end, err := gate.Begin(t.Context(), "first", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	end()
	if identity := gate.PublicIdentity(t.Context()); identity.NativeServerID != "synthetic-realm" || identity.WorkerInstanceID == "" {
		t.Fatalf("first fresh registration not bound: %+v", identity)
	}
	after, err := gate.Observe(t.Context())
	if err != nil || after.NodeID != 7 || after.ActiveRequests != 0 || after.ActiveReservations != 1 {
		t.Fatalf("registered membership: %+v %v", after, err)
	}
}

func TestDrainGateLateFencedSnapshotCannotUndoExplicitCancellation(t *testing.T) {
	for _, variant := range []string{"fenced snapshot", "source error"} {
		t.Run(variant, func(t *testing.T) {
			var fence atomic.Value
			fence.Store("durable-fence")
			var delay atomic.Bool
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			gate := New(func(context.Context) (int, string, string, error) {
				current := fence.Load().(string)
				if delay.CompareAndSwap(true, false) {
					close(entered)
					<-release
					if variant == "source error" {
						return 0, "", "", ErrUnavailable
					}
				}
				return 7, current, "synthetic-realm", nil
			}, nil, nil)
			if proof, err := gate.Observe(t.Context()); err != nil || !proof.Drained {
				t.Fatalf("initial sealed fence: %+v %v", proof, err)
			}
			delay.Store(true)
			done := make(chan error, 1)
			var late Status
			go func() { var err error; late, err = gate.Observe(t.Context()); done <- err }()
			defer func() { unblock(); <-done }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("old fenced observation did not enter source")
			}
			fence.Store("")
			finish, err := gate.Begin(t.Context(), "after-cancellation", time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			finish()
			unblock()
			err = <-done
			done <- err
			if err != nil || late.Fenced || late.Drained || late.ActiveRequests != 0 || late.ActiveReservations != 1 {
				t.Fatalf("old observation undid cancellation: %+v %v", late, err)
			}
		})
	}
}
