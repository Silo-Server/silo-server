package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// TestInstallArchiveKeepDisabledNeverEnables: a restore installs a plugin
// that must not run before its configuration is applied, so the installer
// persists it disabled and never writes an enable, even for a moment.
func TestInstallArchiveKeepDisabledNeverEnables(t *testing.T) {
	store := newRecordingInstallationStore()
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir()})
	manifest := testPluginManifest(t, "test.restore", "1.0.0")
	archivePath := filepath.Join(t.TempDir(), "plugin.zip")
	writePluginArchive(t, archivePath, manifest)
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := installer.installArchive(context.Background(), data, InstallArchiveRequest{PluginID: "test.restore", Version: "1.0.0", KeepDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Installation.Enabled || len(store.createInputs) != 1 || store.createInputs[0].Enabled {
		t.Fatalf("installation created enabled: %+v", store.createInputs)
	}
	for _, in := range store.updateInputs {
		if in.Enabled != nil {
			t.Fatalf("installer wrote enabled=%v", *in.Enabled)
		}
	}
}

func restoreTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRestoreInstallationDB: a plugin restored from a backup is installed
// disabled; its configuration, encrypted under the source installation id,
// is validated like an admin save and re-encrypted under the new id; only
// then does it take the backup's enabled state. Configuration that fails
// validation or cannot be decrypted leaves it disabled, and restoring the
// same plugin again changes nothing.
func TestRestoreInstallationDB(t *testing.T) {
	pool := restoreTestPool(t)
	ctx := t.Context()
	cipher, err := secret.New([]byte("plugin-restore-test-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	const sourceID = 987654
	encode := func(value map[string]any) json.RawMessage {
		raw, err := encodeRuntimeConfigValue(cipher, sourceID, "global", value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	otherKey, _ := secret.New([]byte("another-secret-key-0123456789abcdef!!"))
	foreign, err := encodeRuntimeConfigValue(otherKey, sourceID, "global", map[string]any{"api_key": "abcd"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		configs     []RestoreConfig
		unreadable  bool
		wantEnabled bool
	}{
		{name: "valid configuration", configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "abcd"})}}, wantEnabled: true},
		{name: "fails validation", configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "x"})}}},
		{name: "another key", configs: []RestoreConfig{{Key: "global", Stored: foreign}}},
		{name: "unreadable key check", configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "abcd"})}}, unreadable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			suffix := make([]byte, 4)
			_, _ = rand.Read(suffix)
			pluginID := "test.restore" + hex.EncodeToString(suffix)
			manifest := testPluginManifest(t, pluginID, "1.0.0")
			manifest.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "global",
				JsonSchema: `{"type":"object","properties":{"api_key":{"type":"string","minLength":3}},"required":["api_key"]}`}}
			archivePath := filepath.Join(t.TempDir(), "plugin.zip")
			writePluginArchive(t, archivePath, manifest)
			archive, err := os.ReadFile(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			installations := NewInstallationStore(pool)
			svc := NewService(nil, installations, NewRuntimeConfigStore(pool, cipher), nil,
				NewInstaller(installations, InstallerOptions{BaseDir: t.TempDir()}), nil)
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE plugin_id = $1`, pluginID)
			})
			in := RestoreInstallationInput{PluginID: pluginID, Version: "1.0.0", Enabled: true, Archive: archive,
				SourceInstallationID: sourceID, Configs: tc.configs, ConfigUnreadable: tc.unreadable}
			restore := func() RestoreInstallationResult {
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(ctx) }()
				result, after, err := svc.RestoreInstallation(ctx, tx, in)
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				after(ctx)
				return result
			}
			first := restore()
			got, err := installations.GetByID(ctx, first.InstallationID)
			if err != nil {
				t.Fatal(err)
			}
			if !first.Installed || got.Enabled != tc.wantEnabled || first.Enabled != tc.wantEnabled {
				t.Fatalf("installed %v enabled %v (result %v), want enabled %v; rejected %+v", first.Installed, got.Enabled, first.Enabled, tc.wantEnabled, first.Rejected)
			}
			if !tc.wantEnabled && len(first.Rejected) == 0 {
				t.Fatal("a disabled restore reports no reason")
			}
			if tc.wantEnabled {
				var stored []byte
				if err := pool.QueryRow(ctx, `SELECT config_value FROM plugin_runtime_configs WHERE plugin_installation_id = $1 AND config_key = 'global'`, first.InstallationID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if value, err := decodeRuntimeConfigValue(cipher, first.InstallationID, "global", stored); err != nil || value["api_key"] != "abcd" {
					t.Fatalf("configuration under the new installation id = %v, %v", value, err)
				}
				if _, err := decodeRuntimeConfigValue(cipher, sourceID, "global", stored); err == nil {
					t.Fatal("configuration still decrypts under the source installation id")
				}
			}
			second := restore()
			if second.Changed() || second.InstallationID != first.InstallationID {
				t.Fatalf("restoring again changed something: %+v", second)
			}
		})
	}
}
