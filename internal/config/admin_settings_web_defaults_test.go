package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The web "Restore defaults" control mirrors adminSettingDefaults for the
// worker-pool keys in web/src/pages/admin-settings/settingsWorkerDefaults.ts.
// This keeps the two from drifting: a default changed here fails until the
// mirror is updated.
func TestWebWorkerDefaultsMirrorAdminDefaults(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "pages", "admin-settings", "settingsWorkerDefaults.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	entries := regexp.MustCompile(`"([a-z_.]+)":\s*"([^"]*)"`).FindAllStringSubmatch(string(src), -1)
	if len(entries) == 0 {
		t.Fatalf("no defaults found in %s", path)
	}
	for _, entry := range entries {
		key, web := entry[1], entry[2]
		want, ok := adminSettingDefaults[key]
		if !ok {
			t.Errorf("%s: %q is not an admin setting default", path, key)
			continue
		}
		if web != want {
			t.Errorf("%s: %s = %q, adminSettingDefaults has %q", path, key, web, want)
		}
	}
}
