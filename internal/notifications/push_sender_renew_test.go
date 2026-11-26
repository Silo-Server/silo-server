package notifications

import (
	"context"
	"maps"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// panicSettingReader panics on every read, standing in for a fault between
// taking the relay credential's lock and releasing it.
type panicSettingReader struct{}

func (panicSettingReader) Get(context.Context, string) (string, error) { panic("settings read failed") }

// An alert, a retry or a test push waits for the relay credential's lock as
// long as another send holds it, even when its own context has ended, and
// then goes on; only a profile set wake gives up waiting. Regression: the
// lock's wait gave up for every send whose context ended, which changed how
// alerts behave under a held lock.
func TestRelayCredentialLockWaitsForEverySendButAWake(t *testing.T) {
	store := mapSettingStore{SettingPushRelayURL: DefaultPushRelayURL, SettingPushRelayAPIKey: "relay-key"}
	before := maps.Clone(store)
	var relayed atomic.Int32
	sender := newPushSender(nil, nil, nil, NewSettings(store))
	sender.client = &http.Client{Transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
		relayed.Add(1)
		return relayResponse(http.StatusOK, `{"request_id":"r","status":"accepted"}`), nil
	})}
	device := &PushDevice{Platform: PushPlatformAndroid, ProfileID: "profile-1", ServerDeviceID: "server-device-1"}
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	for _, trigger := range []string{PushTriggerDelivery, PushTriggerTest} {
		sender.renewLock <- struct{}{} // another send holds the lock
		done := make(chan pushSendResult, 1)
		go func() {
			done <- sender.send(ended, PushDeliveryAttempt{ID: "attempt-" + trigger, TriggerType: trigger}, device, "token")
		}()
		// A send that gave up returns at once; this one must still be waiting.
		select {
		case result := <-done:
			t.Fatalf("%s send gave up waiting for the credential lock: %+v", trigger, result)
		case <-time.After(300 * time.Millisecond):
		}
		calls := relayed.Load()
		sender.unlockRenew()
		result := <-done
		if result.UpstreamReason == "relay_credential_unavailable" || relayed.Load() != calls+1 {
			t.Fatalf("%s send after the lock was released = %+v with %d relay calls; want it to go on to the relay", trigger, result, relayed.Load()-calls)
		}
	}
	if !maps.Equal(store, before) {
		t.Fatalf("relay credential settings changed: %v, want %v", store, before)
	}

	// A wake's send gives up when its context ends.
	sender.renewLock <- struct{}{}
	defer sender.unlockRenew()
	if result := sender.send(ended, PushDeliveryAttempt{ID: "wake", TriggerType: PushTriggerProfileSet}, device, "token"); result.UpstreamReason != "relay_credential_unavailable" {
		t.Fatalf("wake send under a held lock = %+v, want relay_credential_unavailable", result)
	}
}

// A panic between taking the relay credential's lock and releasing it
// releases the lock exactly once, so a later send can take it. Regression: a
// lock left held after a panic stops every push for the process; one
// released twice blocks the second release.
func TestRelayCredentialLockIsReleasedAfterAPanic(t *testing.T) {
	sender := newPushSender(nil, nil, nil, NewSettings(panicSettingReader{}))
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		_, _ = sender.prepareRelayCredential(context.Background(), context.Background())
	}()
	select {
	case p := <-done:
		if p == nil {
			t.Fatal("prepareRelayCredential did not panic")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prepareRelayCredential never returned after its panic")
	}
	select {
	case sender.renewLock <- struct{}{}:
		sender.unlockRenew()
	default:
		t.Fatal("the credential lock is still held after the panic")
	}
}
