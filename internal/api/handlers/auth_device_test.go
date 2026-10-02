package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
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

// A request the device canceled through /api/v2 keeps the frozen /api/v1
// values: status denied, and the v1 denied conflict code on decisions.
func TestDeviceLoginV1KeepsCanceledAsDenied(t *testing.T) {
	t.Parallel()

	if got := v1DeviceStatus(auth.DeviceLoginStatusCanceled); got != auth.DeviceLoginStatusDenied {
		t.Fatalf("v1 status = %q, want denied", got)
	}
	for _, status := range []string{auth.DeviceLoginStatusPending, auth.DeviceLoginStatusApproved, auth.DeviceLoginStatusDenied, auth.DeviceLoginStatusConsumed, auth.DeviceLoginStatusExpired} {
		if got := v1DeviceStatus(status); got != status {
			t.Fatalf("v1 status %q = %q", status, got)
		}
	}
	apiErr := deviceDecisionError(auth.ErrDeviceLoginCanceled)
	if apiErr.Status != http.StatusConflict || apiErr.Code != "denied" {
		t.Fatalf("decision error = %d %q", apiErr.Status, apiErr.Code)
	}
}

// Device decisions need a login session: an approval gives the device a
// login session, which an API key or an impersonation session must not
// mint. The frozen v1 routes answer with v1's existing 403 forbidden code.
func TestDeviceDecisionsRequireLoginSession(t *testing.T) {
	t.Parallel()

	impersonator := 2
	h := &AuthHandler{device: &auth.DeviceLoginService{}}
	for name, claims := range map[string]*auth.Claims{
		"api key":       {UserID: 1, TokenType: auth.TokenTypeAPIKey},
		"impersonation": {UserID: 1, TokenType: auth.TokenTypeAccess, SessionID: "s-imp", ImpersonatorUserID: &impersonator},
	} {
		for route, handle := range map[string]http.HandlerFunc{
			"approve": h.HandleDeviceApprove,
			"deny":    h.HandleDeviceDeny,
		} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device/"+route, strings.NewReader(`{"code":"4821-7730"}`))
			req = req.WithContext(apimw.SetClaims(req.Context(), claims))
			handle(rec, req)
			var body struct {
				Error string `json:"error"`
			}
			if rec.Code != http.StatusForbidden || json.NewDecoder(rec.Body).Decode(&body) != nil || body.Error != "forbidden" {
				t.Fatalf("%s %s: %d %s", name, route, rec.Code, rec.Body.String())
			}
		}
		ctx := apimw.SetClaims(context.Background(), claims)
		var apiErr *APIError
		if _, err := h.ApproveDeviceHandoff(ctx, auth.DeviceLoginLookupInput{UserCode: "4821-7730"}, 1, "p-1"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
			t.Fatalf("%s handoff: %v", name, err)
		}
	}
}

func TestDeviceDecisionRevokedSessionResponse(t *testing.T) {
	t.Parallel()

	err := deviceDecisionError(auth.ErrSessionRevoked)
	if !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatal("device decision lost the revoked-session cause")
	}
	rec := httptest.NewRecorder()
	writeAPIError(rec, err)
	var body struct {
		Error string `json:"error"`
	}
	if rec.Code != http.StatusUnauthorized || json.NewDecoder(rec.Body).Decode(&body) != nil || body.Error != "unauthorized" {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
