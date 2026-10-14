package playback

import (
	"errors"
	"testing"
)

func TestStreamDenyStatusReadsLiveNegativeAndPreservesUnknown(t *testing.T) {
	deny, client, _, _ := newTestStreamDeny(t)
	if denied, err := deny.DeniedStatus(t.Context(), "status-work"); err != nil || denied {
		t.Fatal("fresh missing marker was not a definite negative")
	}
	client.setErr(errors.New("redis unavailable"))
	if _, err := deny.DeniedStatus(t.Context(), "status-work"); err == nil {
		t.Fatal("cached negative hid unavailable live authority")
	}
	client.setErr(nil)
	client.mu.Lock()
	client.keys[StreamDenyKey("status-work")] = fakeStreamDenyKey{value: "deny", ttl: MaxTokenTTL}
	client.mu.Unlock()
	if denied, err := deny.DeniedStatus(t.Context(), "status-work"); err != nil || !denied {
		t.Fatal("fresh cross-replica revocation was not observed")
	}
	client.setErr(errors.New("redis unavailable"))
	deny.Deny(t.Context(), "local-revoke")
	if denied, err := deny.DeniedStatus(t.Context(), "local-revoke"); err != nil || !denied {
		t.Fatal("failed marker write lost immediate local revoke")
	}
}
