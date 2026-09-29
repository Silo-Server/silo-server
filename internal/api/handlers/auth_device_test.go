package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
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

func TestV1DeviceLoginKeepsItsStatusVocabulary(t *testing.T) {
	for status, want := range map[string]string{
		auth.DeviceLoginStatusCancelled: "expired",
		auth.DeviceLoginStatusPending:   auth.DeviceLoginStatusPending,
		auth.DeviceLoginStatusDenied:    auth.DeviceLoginStatusDenied,
		"expired":                       "expired",
	} {
		if got := v1DeviceStatus(status); got != want {
			t.Fatalf("v1DeviceStatus(%q) = %q, want %q", status, got, want)
		}
	}
	var apiErr *APIError
	if err := v1DeviceDecisionError(deviceDecisionError(auth.ErrDeviceLoginCancelled)); !errors.As(err, &apiErr) || apiErr.Status != http.StatusGone || apiErr.Code != "expired" {
		t.Fatalf("v1 decision on a withdrawn request = %v, want the v1 410 expired", err)
	}
	denied := deviceDecisionError(auth.ErrDeviceLoginDenied)
	if err := v1DeviceDecisionError(denied); !errors.Is(err, denied) {
		t.Fatalf("other decision errors changed: %v", err)
	}
}
