package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAdminJellyfinStatusSharedRead(t *testing.T) {
	h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"jellyfin_compat.enabled": "false", "jellyfin_compat.web_install_dir": t.TempDir(), "jellyfin_compat.server_name": "Synthetic server"}}}
	status, err := h.ReadAdminJellyfinCompatStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.ServerName != "Synthetic server" {
		t.Fatal(status)
	}
	rec := httptest.NewRecorder()
	h.HandleGetJellyfinCompatStatus(rec, httptest.NewRequest("GET", "/", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body["server_name"] != status.ServerName || body["api_state"] != status.APIState {
		t.Fatal(rec.Code, body)
	}
	h.SettingsRepo = nil
	if _, err := h.ReadAdminJellyfinCompatStatus(t.Context()); err == nil {
		t.Fatal("missing settings accepted")
	}
}
