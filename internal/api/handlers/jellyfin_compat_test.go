package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
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

func TestUpdateJellyfinCompatSettingsUpdatesWebEnabled(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	settings := &fakeServerSettingsStore{values: map[string]string{
		"jellyfin_compat.web_install_dir": "/var/lib/silo/compat/jellyfin-web",
	}}
	restartStatus := NewServerRestartStatusTracker()
	handler := &AdminHandler{
		Config:        cfg,
		SettingsRepo:  settings,
		RestartStatus: restartStatus,
	}

	req := httptest.NewRequest(
		http.MethodPatch,
		"/admin/jellyfin-compat/settings",
		strings.NewReader(`{"web_enabled":false}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateJellyfinCompatSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := settings.values["jellyfin_compat.web_enabled"]; got != "false" {
		t.Fatalf("jellyfin_compat.web_enabled = %q, want false", got)
	}
	if !strings.Contains(rec.Body.String(), `"web_enabled":false`) {
		t.Fatalf("response body %q does not include disabled web_enabled", rec.Body.String())
	}
	if snapshot := restartStatus.Snapshot(); snapshot.RestartRequired {
		t.Fatalf("RestartRequired = true, want false for web_enabled-only update")
	}
}

func TestUpdateJellyfinCompatSettingsDoesNotMarkRestartForLiveFields(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	root := t.TempDir()
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	restartStatus := NewServerRestartStatusTracker()
	handler := &AdminHandler{
		Config:        cfg,
		SettingsRepo:  settings,
		RestartStatus: restartStatus,
	}

	body := `{"public_url":"https://compat.example.test","server_name":"Silo Compat","emulated_server_version":"10.11.6","web_version":"10.11.6","web_install_dir":"` + root + `"}`
	req := httptest.NewRequest(
		http.MethodPatch,
		"/admin/jellyfin-compat/settings",
		strings.NewReader(body),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateJellyfinCompatSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := settings.values["jellyfin_compat.public_url"]; got != "https://compat.example.test" {
		t.Fatalf("jellyfin_compat.public_url = %q, want updated value", got)
	}
	if got := settings.values["jellyfin_compat.server_name"]; got != "Silo Compat" {
		t.Fatalf("jellyfin_compat.server_name = %q, want updated value", got)
	}
	if got := settings.values["jellyfin_compat.emulated_server_version"]; got != "10.11.6" {
		t.Fatalf("jellyfin_compat.emulated_server_version = %q, want 10.11.6", got)
	}
	if got := settings.values["jellyfin_compat.web_version"]; got != "10.11.6" {
		t.Fatalf("jellyfin_compat.web_version = %q, want 10.11.6", got)
	}
	if got := settings.values["jellyfin_compat.web_install_dir"]; got != root {
		t.Fatalf("jellyfin_compat.web_install_dir = %q, want %q", got, root)
	}
	if snapshot := restartStatus.Snapshot(); snapshot.RestartRequired {
		t.Fatalf("RestartRequired = true, want false for live Jellyfin settings")
	}
}

func TestJellyfinCompatSettingsRequireRestartFollowsRestartRegistry(t *testing.T) {
	cases := []struct {
		name    string
		updates map[string]string
		want    bool
	}{
		{
			name: "listener setting",
			updates: map[string]string{
				"jellyfin_compat.enabled": "true",
			},
			want: true,
		},
		{
			name: "live identity settings",
			updates: map[string]string{
				"jellyfin_compat.public_url":              "https://compat.example.test",
				"jellyfin_compat.server_name":             "Silo Compat",
				"jellyfin_compat.emulated_server_version": "10.11.6",
			},
		},
		{
			name: "live web settings",
			updates: map[string]string{
				"jellyfin_compat.web_enabled":     "false",
				"jellyfin_compat.web_version":     "10.11.6",
				"jellyfin_compat.web_dir":         "/var/lib/silo/compat/jellyfin-web/current",
				"jellyfin_compat.web_install_dir": "/var/lib/silo/compat/jellyfin-web",
			},
		},
		{
			name: "mixed live and listener settings",
			updates: map[string]string{
				"jellyfin_compat.enabled":     "false",
				"jellyfin_compat.web_enabled": "false",
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jellyfinCompatSettingsRequireRestart(tc.updates); got != tc.want {
				t.Fatalf("jellyfinCompatSettingsRequireRestart() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateJellyfinCompatSettingsDisablesWebWhenAPIDisabled(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	settings := &fakeServerSettingsStore{values: map[string]string{
		"jellyfin_compat.enabled":     "true",
		"jellyfin_compat.web_enabled": "true",
	}}
	restartStatus := NewServerRestartStatusTracker()
	published := map[string]string{}
	handler := &AdminHandler{
		Config:        cfg,
		SettingsRepo:  settings,
		RestartStatus: restartStatus,
		OnServerSettingUpdated: func(_ context.Context, key, value string) {
			published[key] = value
		},
	}

	req := httptest.NewRequest(
		http.MethodPatch,
		"/admin/jellyfin-compat/settings",
		strings.NewReader(`{"enabled":false,"web_enabled":true}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateJellyfinCompatSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := settings.values["jellyfin_compat.enabled"]; got != "false" {
		t.Fatalf("jellyfin_compat.enabled = %q, want false", got)
	}
	if got := settings.values["jellyfin_compat.web_enabled"]; got != "false" {
		t.Fatalf("jellyfin_compat.web_enabled = %q, want false", got)
	}
	if got := published["jellyfin_compat.web_enabled"]; got != "false" {
		t.Fatalf("published jellyfin_compat.web_enabled = %q, want false", got)
	}
	if !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("response body %q does not include disabled API", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"web_enabled":false`) {
		t.Fatalf("response body %q does not include disabled web_enabled", rec.Body.String())
	}
	if snapshot := restartStatus.Snapshot(); !snapshot.RestartRequired {
		t.Fatal("RestartRequired = false, want true for API setting update")
	}
}

func TestRemoveJellyfinCompatWebDisablesWebSetting(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	settings := &fakeServerSettingsStore{values: map[string]string{
		"jellyfin_compat.enabled":         "true",
		"jellyfin_compat.web_enabled":     "true",
		"jellyfin_compat.web_install_dir": t.TempDir(),
	}}
	published := map[string]string{}
	handler := &AdminHandler{
		Config:       cfg,
		SettingsRepo: settings,
		OnServerSettingUpdated: func(_ context.Context, key, value string) {
			published[key] = value
		},
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/jellyfin-compat/web/remove",
		strings.NewReader(`{}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleRemoveJellyfinCompatWeb(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if got := settings.values["jellyfin_compat.web_enabled"]; got != "false" {
		t.Fatalf("jellyfin_compat.web_enabled = %q, want false", got)
	}
	if got := published["jellyfin_compat.web_enabled"]; got != "false" {
		t.Fatalf("published jellyfin_compat.web_enabled = %q, want false", got)
	}
	if !strings.Contains(rec.Body.String(), `"web_enabled":false`) {
		t.Fatalf("response body %q does not include disabled web_enabled", rec.Body.String())
	}
}

func TestUpdateJellyfinCompatSettingsMarksRestartForProxyChanges(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	restartStatus := NewServerRestartStatusTracker()
	handler := &AdminHandler{
		Config:        cfg,
		SettingsRepo:  settings,
		RestartStatus: restartStatus,
	}

	req := httptest.NewRequest(
		http.MethodPatch,
		"/admin/jellyfin-compat/settings",
		strings.NewReader(`{"enabled":true}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateJellyfinCompatSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := settings.values["jellyfin_compat.enabled"]; got != "true" {
		t.Fatalf("jellyfin_compat.enabled = %q, want true", got)
	}
	snapshot := restartStatus.Snapshot()
	if !snapshot.RestartRequired {
		t.Fatal("RestartRequired = false, want true for proxy setting update")
	}
	if snapshot.RestartRequiredReason != "jellyfin_compat" {
		t.Fatalf("RestartRequiredReason = %q, want jellyfin_compat", snapshot.RestartRequiredReason)
	}
}

func TestPersistJellyfinCompatWebInstallSettingsEnablesWebUI(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		"jellyfin_compat.web_enabled": "false",
	}}
	handler := &AdminHandler{SettingsRepo: settings}

	err := handler.persistJellyfinCompatWebInstallSettings(context.Background(), jellycompat.WebComponentStatus{
		InstallRoot:      "/var/lib/silo/compat/jellyfin-web",
		PinnedVersion:    "10.11.6",
		InstalledVersion: "10.11.6",
		SourceURL:        jellycompat.DefaultWebSourceURL,
	})
	if err != nil {
		t.Fatalf("persistJellyfinCompatWebInstallSettings: %v", err)
	}

	if got := settings.values["jellyfin_compat.web_enabled"]; got != "true" {
		t.Fatalf("jellyfin_compat.web_enabled = %q, want true", got)
	}
	if got := settings.values["jellyfin_compat.web_install_dir"]; got != "/var/lib/silo/compat/jellyfin-web" {
		t.Fatalf("jellyfin_compat.web_install_dir = %q", got)
	}
	if got := settings.values["jellyfin_compat.web_dir"]; got != jellycompat.ManagedWebInstallPath("/var/lib/silo/compat/jellyfin-web") {
		t.Fatalf("jellyfin_compat.web_dir = %q", got)
	}
	if got := settings.values["jellyfin_compat.web_version"]; got != "10.11.6" {
		t.Fatalf("jellyfin_compat.web_version = %q, want 10.11.6", got)
	}
	if got := settings.values["jellyfin_compat.web_source_url"]; got != jellycompat.DefaultWebSourceURL {
		t.Fatalf("jellyfin_compat.web_source_url = %q", got)
	}
}
