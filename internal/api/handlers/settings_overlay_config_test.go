package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func readOverlayConfig(t *testing.T, handler *SettingsHandler) overlayConfigResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.HandleGetOverlayConfig(
		recorder,
		httptest.NewRequest(http.MethodGet, "/settings/overlay-config", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response overlayConfigResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func TestGetOverlayConfigIncludesQuickActionDefaults(t *testing.T) {
	response := readOverlayConfig(t, NewSettingsHandler(nil))
	if !response.Enabled {
		t.Fatal("overlays enabled = false, want true")
	}
	if response.QuickActionsEnabled {
		t.Fatal("card quick actions enabled = true, want false")
	}
	if response.QuickActionsDefault != "both" {
		t.Fatalf("card quick actions default = %q, want both", response.QuickActionsDefault)
	}
}

func TestGetOverlayConfigIncludesQuickActionAdminDefaults(t *testing.T) {
	handler := NewSettingsHandler(nil)
	handler.SetServerSettings(&fakeServerSettingsStore{values: map[string]string{
		"overlays.enabled":                    "false",
		"defaults.card_overlays":              `{"preset":"classic"}`,
		"defaults.card_quick_actions_enabled": "false",
		"defaults.card_quick_actions":         "favorites",
	}})

	response := readOverlayConfig(t, handler)
	if response.Enabled {
		t.Fatal("overlays enabled = true, want false")
	}
	if response.Defaults != `{"preset":"classic"}` {
		t.Fatalf("defaults = %q", response.Defaults)
	}
	if response.QuickActionsEnabled {
		t.Fatal("card quick actions enabled = true, want false")
	}
	if response.QuickActionsDefault != "favorites" {
		t.Fatalf("card quick actions default = %q, want favorites", response.QuickActionsDefault)
	}
}

func TestGetOverlayConfigEnablesQuickActionsWhenDefaultStoredTrue(t *testing.T) {
	handler := NewSettingsHandler(nil)
	handler.SetServerSettings(&fakeServerSettingsStore{values: map[string]string{
		"defaults.card_quick_actions_enabled": "true",
	}})

	response := readOverlayConfig(t, handler)
	if !response.QuickActionsEnabled {
		t.Fatal("card quick actions enabled = false, want true")
	}
	if response.QuickActionsDefault != "both" {
		t.Fatalf("card quick actions default = %q, want both", response.QuickActionsDefault)
	}
}
