package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

func TestAdminJellyfinSettingsAtomicGuard(t *testing.T) {
	root := t.TempDir()
	store := newSerializedSettingsStore(map[string]string{"jellyfin_compat.enabled": "true", "jellyfin_compat.web_enabled": "true", "jellyfin_compat.web_install_dir": root, "server.log_level": "info"})
	callbacks := 0
	h := &AdminHandler{SettingsRepo: store, OnServerSettingUpdated: func(context.Context, string, string) { callbacks++ }}
	stale := errors.New("stale displayed settings")
	patch := AdminJellyfinCompatSettingsPatch{Enabled: new(false), WebEnabled: new(true)}
	_, err := h.UpdateAdminJellyfinCompatSettings(t.Context(), patch, func(AdminSettingsSnapshot) error { return stale })
	if !errors.Is(err, stale) || callbacks != 0 || store.values["jellyfin_compat.enabled"] != "true" {
		t.Fatal("stale patch had effects", err)
	}
	guard := func(snapshot AdminSettingsSnapshot) error {
		if snapshot.Stored["server.log_level"] != "info" {
			t.Fatal("guard lost unrelated setting")
		}
		return nil
	}
	_, err = h.UpdateAdminJellyfinCompatSettings(t.Context(), patch, guard)
	if err != nil || store.values["jellyfin_compat.enabled"] != "false" || store.values["jellyfin_compat.web_enabled"] != "false" || callbacks != 2 || store.directSets != 0 {
		t.Fatal("atomic coupled disable failed", err, store.values, callbacks)
	}
	_, err = h.UpdateAdminJellyfinCompatSettings(t.Context(), AdminJellyfinCompatSettingsPatch{WebDir: new("/arbitrary-path"), ServerName: new("must not commit")}, guard)
	if err == nil || store.values["jellyfin_compat.server_name"] != "" || callbacks != 2 {
		t.Fatal("invalid managed directory had effects")
	}
	_, err = h.UpdateAdminJellyfinCompatSettings(t.Context(), AdminJellyfinCompatSettingsPatch{WebDir: new("")}, guard)
	if err != nil || store.values["jellyfin_compat.web_dir"] != jellycompat.ManagedWebInstallPath(root) {
		t.Fatal("managed directory did not use locked root", err)
	}
}
