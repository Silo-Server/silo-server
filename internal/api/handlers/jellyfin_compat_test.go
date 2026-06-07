package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestUpdateJellyfinCompatSettingsRejectsArbitraryWebDir(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	handler := &AdminHandler{
		Config: cfg,
		SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
			"jellyfin_compat.web_install_dir": "/var/lib/silo/compat/jellyfin-web",
		}},
	}

	req := httptest.NewRequest(
		http.MethodPatch,
		"/admin/jellyfin-compat/settings",
		strings.NewReader(`{"web_dir":"/etc"}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateJellyfinCompatSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "cannot point at an arbitrary path") {
		t.Fatalf("unexpected response body %q", rec.Body.String())
	}
}
