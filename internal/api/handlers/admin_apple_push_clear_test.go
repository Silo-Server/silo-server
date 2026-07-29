package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

func TestAdminApplePushHandlerClearsRelayCredentialAtomically(t *testing.T) {
	fixture := "clawrouter-e2e-secret"
	settings := &fakeServerSettingsStore{values: map[string]string{
		notifications.SettingPushRelayURL:          "https://push.siloserver.org",
		notifications.SettingPushRelayDeploymentID: "deployment-existing",
		notifications.SettingPushRelayAPIKey:       fixture,
		notifications.SettingPushRelayKeyPrefix:    "cap_v1_existing",
		notifications.SettingPushRelayExpiresAt:    "2026-08-01T00:00:00Z",
		notifications.SettingPushRelayReregister:   "true",
	}}
	h := NewAdminApplePushHandler(
		&notifications.System{Settings: notifications.NewSettings(settings)},
		settings,
	)
	rec := httptest.NewRecorder()

	h.HandleClearRelay(
		rec,
		httptest.NewRequest(http.MethodDelete, "/admin/notifications/push/relay", nil),
	)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", rec.Code, rec.Body.String())
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
	if settings.values[notifications.SettingPushRelayReregister] != "false" {
		t.Fatalf(
			"reregistration marker = %q, want false",
			settings.values[notifications.SettingPushRelayReregister],
		)
	}
}
