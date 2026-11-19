package handlers

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
)

func readEnv(t *testing.T, env map[string]string) config.EnvSettings {
	t.Helper()
	settings, err := config.ReadEnvSettings(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestCheckEnvironmentSettings(t *testing.T) {
	checked, err := CheckEnvironmentSettings(nil, readEnv(t, map[string]string{"SILO_TRUSTED_PROXIES": " 10.0.0.0/8 ,203.0.113.7/32 "}), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := checked.All()["clientip.trusted_proxies"]; got != "10.0.0.0/8, 203.0.113.7/32" {
		t.Errorf("trusted proxies = %q, want the list an admin save stores", got)
	}

	for name, env := range map[string]map[string]string{
		"per-key":     {"SILO_TRUSTED_PROXIES": "not-a-cidr"},
		"cross-field": {"SILO_ARTWORK_STORAGE": "s3"},
	} {
		_, err := CheckEnvironmentSettings(nil, readEnv(t, env), false)
		for variable := range env {
			if err == nil || !strings.Contains(err.Error(), variable) {
				t.Errorf("%s: error = %v, want one naming %s", name, err, variable)
			}
		}
	}
}

// Once artwork is written, its location moves only through a managed
// transition, from the environment as from an admin save.
func TestCheckEnvironmentSettingsKeepsRecordedStorage(t *testing.T) {
	root := t.TempDir()
	stored := map[string]string{
		"artwork.storage_backend":    "local",
		"artwork.local_path":         root,
		blobstore.IdentitySettingKey: "local|" + root,
	}
	_, err := CheckEnvironmentSettings(stored, readEnv(t, map[string]string{"SILO_ARTWORK_PATH": t.TempDir()}), false)
	if err == nil || !strings.Contains(err.Error(), "SILO_ARTWORK_PATH") {
		t.Errorf("moving recorded storage: error = %v, want a refusal naming SILO_ARTWORK_PATH", err)
	}
	if _, err := CheckEnvironmentSettings(stored, readEnv(t, map[string]string{"SILO_ARTWORK_PATH": root, "SILO_S3_PUBLIC_REGION": "eu-west-1"}), false); err != nil {
		t.Errorf("recorded location with other storage settings: %v", err)
	}
}
