package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// Clearing removes every credential row in one write and parks first-use
// registration until an administrator registers again.
func TestClearNotificationRelayParksRegistration(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		notifications.SettingPushRelayURL:          "https://push.siloserver.org",
		notifications.SettingPushRelayDeploymentID: "deployment-existing",
		notifications.SettingPushRelayAPIKey:       "clawrouter-e2e-secret",
		notifications.SettingPushRelayKeyPrefix:    "cap_v1_existing",
		notifications.SettingPushRelayExpiresAt:    "2026-08-01T00:00:00Z",
		notifications.SettingPushRelayReregister:   "true",
	}}
	h := NewAdminApplePushHandler(&notifications.System{Settings: notifications.NewSettings(settings)}, settings)

	if err := h.ClearNotificationRelay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if settings.setManyCalls != 1 {
		t.Fatalf("SetMany calls = %d, want 1", settings.setManyCalls)
	}
	for _, key := range []string{
		notifications.SettingPushRelayURL,
		notifications.SettingPushRelayDeploymentID,
		notifications.SettingPushRelayAPIKey,
		notifications.SettingPushRelayKeyPrefix,
		notifications.SettingPushRelayExpiresAt,
	} {
		if settings.values[key] != "" {
			t.Fatalf("%s = %q, want empty", key, settings.values[key])
		}
	}
	if settings.values[notifications.SettingPushRelayReregister] != "true" {
		t.Fatalf("reregistration marker = %q, want true", settings.values[notifications.SettingPushRelayReregister])
	}
}

// With a stored capability, registering rotates it; when the relay no longer
// accepts that capability, registering falls back to a fresh deployment.
func TestRegisterNotificationRelayRotatesOrReplacesTheCapability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		rotateRejected bool
		wantPaths      []string
		wantKey        string
		wantDeployment string
	}{
		{name: "rotation", wantPaths: []string{"/v1/deployments/rotate"}, wantKey: "rotated.capability.value", wantDeployment: "deployment-existing"},
		{name: "rejected rotation", rotateRejected: true, wantPaths: []string{"/v1/deployments/rotate", "/v1/deployments/register"}, wantKey: "fresh.capability.value", wantDeployment: "deployment-fresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: map[string]string{
				notifications.SettingPushRelayDeploymentID: "deployment-existing",
				notifications.SettingPushRelayAPIKey:       "existing.capability.value",
				notifications.SettingPushRelayKeyPrefix:    "cap_v1_existing",
				notifications.SettingPushRelayExpiresAt:    "2026-08-01T00:00:00Z",
			}}
			var paths []string
			var idempotencyKey string
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/v1/deployments/rotate":
					if got := r.Header.Get("Authorization"); got != "Bearer existing.capability.value" {
						t.Errorf("rotation Authorization = %q", got)
					}
					idempotencyKey = r.Header.Get("Idempotency-Key")
					if tc.rotateRejected {
						writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "capability_revoked", "message": "rejected"}})
						return
					}
					writeJSON(w, http.StatusOK, map[string]any{
						"request_id": "rotate-request", "deployment_id": "deployment-existing",
						"api_key": "rotated.capability.value", "key_prefix": "cap_v1_rotated", "expires_at": "2026-09-01T00:00:00Z",
					})
				case "/v1/deployments/register":
					writeJSON(w, http.StatusOK, map[string]any{
						"request_id": "register-request", "deployment_id": "deployment-fresh",
						"api_key": "fresh.capability.value", "key_prefix": "cap_v2_fresh", "expires_at": "2026-09-01T00:00:00Z",
					})
				default:
					t.Errorf("relay path = %q", r.URL.Path)
				}
			}))
			t.Cleanup(relay.Close)
			settings.values[notifications.SettingPushRelayURL] = relay.URL
			h := NewAdminApplePushHandler(&notifications.System{Settings: notifications.NewSettings(settings)}, settings)
			h.client = relay.Client()
			h.developmentRelayURL = relay.URL

			view, err := h.RegisterNotificationRelay(t.Context(), relay.URL)
			if err != nil {
				t.Fatal(err)
			}
			if len(paths) != len(tc.wantPaths) || paths[0] != tc.wantPaths[0] || paths[len(paths)-1] != tc.wantPaths[len(tc.wantPaths)-1] {
				t.Fatalf("relay paths = %v, want %v", paths, tc.wantPaths)
			}
			if idempotencyKey == "" {
				t.Fatal("rotation omitted Idempotency-Key")
			}
			if got := settings.values[notifications.SettingPushRelayAPIKey]; got != tc.wantKey {
				t.Fatalf("stored capability = %q, want %q", got, tc.wantKey)
			}
			if view.DeploymentID != tc.wantDeployment || settings.values[notifications.SettingPushRelayDeploymentID] != tc.wantDeployment {
				t.Fatalf("deployment = view %q stored %q, want %q", view.DeploymentID, settings.values[notifications.SettingPushRelayDeploymentID], tc.wantDeployment)
			}
			if tc.rotateRejected && settings.values[notifications.SettingPushRelayReregister] != "false" {
				t.Fatalf("reregistration marker = %q, want false after a fresh registration", settings.values[notifications.SettingPushRelayReregister])
			}
		})
	}
}

// A relay rate limit is reported with its wait, and nothing is stored.
func TestRegisterNotificationRelayMapsRelayRateLimit(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]string{"code": "rate_limited", "message": "too many deployment registrations from this network"},
		})
	}))
	t.Cleanup(relay.Close)
	h := NewAdminApplePushHandler(&notifications.System{}, settings)
	h.client = relay.Client()
	h.developmentRelayURL = relay.URL

	_, err := h.RegisterNotificationRelay(t.Context(), relay.URL)
	requireAdminError(t, err, http.StatusTooManyRequests, "relay_rate_limited")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 60 {
		t.Fatalf("err = %v, want an APIError with RetryAfter 60", err)
	}
	if settings.values[notifications.SettingPushRelayAPIKey] != "" {
		t.Fatal("api key was stored after relay rejected registration")
	}
}
