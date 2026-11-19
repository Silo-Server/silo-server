package handlers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A local artwork root that startup cannot open makes every restart exit
// before the admin UI is reachable, so the save refuses it.
func TestArtworkLocalPathRejectsRootStartupCannotOpen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"file": file, "below a file": filepath.Join(file, "artwork")} {
		store := &fakeServerSettingsStore{values: map[string]string{}}
		h := &AdminHandler{SettingsRepo: store}
		_, batchErr := h.UpdateAdminSettings(t.Context(), map[string]string{"artwork.local_path": path}, nil)
		_, singleErr := h.UpdateAdminSetting(t.Context(), "artwork.local_path", path, nil)
		for route, err := range map[string]error{"batch": batchErr, "single": singleErr} {
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != 400 || apiErr.Field == "" {
				t.Fatalf("%s %s: %v, want a 400 field error", name, route, err)
			}
		}
		if _, saved := store.values["artwork.local_path"]; saved {
			t.Fatalf("%s: refused path was saved", name)
		}
	}
	// A root that does not exist yet is accepted and not created by the save.
	missing := filepath.Join(t.TempDir(), "new", "artwork")
	if _, err := (&AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}}}).UpdateAdminSettings(t.Context(), map[string]string{"artwork.local_path": missing}, nil); err != nil {
		t.Fatalf("missing root: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("save created the root's parent: %v", err)
	}
}
