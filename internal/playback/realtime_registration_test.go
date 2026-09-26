package playback

import (
	"errors"
	"testing"
)

type registrationTestConn struct{}

func (registrationTestConn) WriteJSON(any) error { return nil }

func TestCurrentRealtimeRegistrationRefusesStaleConnections(t *testing.T) {
	hub := NewRealtimeHub()
	old := hub.Register("session", registrationTestConn{})
	current := hub.Register("session", registrationTestConn{})
	calls := 0
	apply := func() error { calls++; return nil }
	for _, reg := range []*RealtimeRegistration{nil, old} {
		if err := hub.WithCurrentRegistration(reg, apply); !errors.Is(err, ErrStaleRealtimeRegistration) {
			t.Fatalf("stale registration: %v", err)
		}
	}
	if err := NewRealtimeHub().WithCurrentRegistration(current, apply); !errors.Is(err, ErrStaleRealtimeRegistration) {
		t.Fatalf("foreign hub registration: %v", err)
	}
	if calls != 0 {
		t.Fatal("stale callback executed")
	}
	want := errors.New("authority refused")
	if err := hub.WithCurrentRegistration(current, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error lost: %v", err)
	}
	if err := hub.WithCurrentRegistration(current, apply); err != nil || calls != 1 {
		t.Fatalf("current callback: calls=%d err=%v", calls, err)
	}
	if !hub.Unregister(current) {
		t.Fatal("unregister current")
	}
	replacement := hub.Register("session", registrationTestConn{})
	if err := hub.WithCurrentRegistration(current, apply); !errors.Is(err, ErrStaleRealtimeRegistration) || calls != 1 {
		t.Fatalf("deleted lane admitted old callback: %v", err)
	}
	if err := hub.WithCurrentRegistration(replacement, apply); err != nil || calls != 2 {
		t.Fatalf("new lane: %v", err)
	}
}

func TestCurrentRealtimeRegistrationSerializesReplacement(t *testing.T) {
	hub := NewRealtimeHub()
	old := hub.Register("session", registrationTestConn{})
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- hub.WithCurrentRegistration(old, func() error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-t.Context().Done():
				return t.Context().Err()
			}
		})
	}()
	<-entered
	lookup := make(chan struct{})
	hub.onRegisterLaneLookup = func(string, *sessionLane) { close(lookup) }
	replaced := make(chan *RealtimeRegistration, 1)
	go func() { replaced <- hub.Register("session", registrationTestConn{}) }()
	<-lookup
	// The callback holds the lane: replacement cannot become visible until it
	// finishes. Observe that lock directly rather than relying on a sleep.
	if old.lane.mu.TryLock() {
		old.lane.mu.Unlock()
		t.Fatal("callback did not retain lane ownership")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current := <-replaced
	if current == nil {
		t.Fatal("replacement failed")
	}
	if err := hub.WithCurrentRegistration(old, func() error { t.Error("superseded callback executed"); return nil }); !errors.Is(err, ErrStaleRealtimeRegistration) {
		t.Fatalf("superseded registration: %v", err)
	}
	if hub.Unregister(old) {
		t.Fatal("old disconnect removed successor")
	}
}
