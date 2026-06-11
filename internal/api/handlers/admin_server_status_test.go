package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestAdminServerStatusClearsAfterProcessRestart(t *testing.T) {
	t.Parallel()

	restartStatus := NewServerRestartStatusTracker()
	restartStatus.MarkRequired("settings")

	handler := &AdminHandler{RestartStatus: restartStatus}
	req := httptest.NewRequest(http.MethodGet, "/admin/server/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.RestartRequired {
		t.Fatal("RestartRequired = false, want true")
	}

	restartedHandler := &AdminHandler{RestartStatus: NewServerRestartStatusTracker()}
	rec = httptest.NewRecorder()
	restartedHandler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status after restart = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response after restart: %v", err)
	}
	if resp.RestartRequired {
		t.Fatal("RestartRequired = true after new process tracker, want false")
	}
}

func TestAdminServerStatusDoesNotPromoteLiveJellyfinIdentitySettings(t *testing.T) {
	t.Parallel()

	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":                 "true",
		"jellyfin_compat.listen":                  ":8096",
		"jellyfin_compat.public_url":              "http://127.0.0.1:8096",
		"jellyfin_compat.server_name":             "Silo",
		"jellyfin_compat.emulated_server_version": "10.11.0",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	handler := &AdminHandler{
		Config:        cfg,
		RestartStatus: NewServerRestartStatusTracker(),
		SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
			"jellyfin_compat.public_url":              "https://compat.example.test",
			"jellyfin_compat.server_name":             "Silo Compat",
			"jellyfin_compat.emulated_server_version": "10.11.6",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/server/status", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RestartRequired {
		t.Fatal("RestartRequired = true, want false for live Jellyfin identity settings")
	}
}
