package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceLoginCapabilityAdvertisesRemotePlaybackHandoff(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/device/capability", nil)
	new(AuthHandler).HandleDeviceCapability(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var response deviceLoginCapabilityResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode capability response: %v", err)
	}
	if !response.RemotePlaybackHandoff {
		t.Fatal("remote_playback_handoff = false, want true")
	}
	if len(response.ProtocolVersions) != 1 || response.ProtocolVersions[0] != 2 {
		t.Fatalf("protocol_versions = %v, want [2]", response.ProtocolVersions)
	}
}
