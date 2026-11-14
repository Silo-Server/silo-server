package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/secret"
)

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

// TestRestoreInstallationDB: a restore never installs a plugin: one missing
// here is refused (ErrPluginNotInstalled) and nothing is created. For an
// installed plugin, its configuration, encrypted under the source
// installation id, is validated like an admin save and re-encrypted under
// this installation's id; configuration that fails validation or cannot be
// decrypted is left out with its reason; the plugin keeps its enabled
// state; and restoring the same plugin again changes nothing.
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
	f := newRestoreFixture(t, pool, cipher)
	installations, svc, restore, newPluginID := f.installations, f.svc, f.restore, f.newPluginID
	t.Run("not installed", func(t *testing.T) {
		pluginID := newPluginID()
		_, err := restore(RestoreInstallationInput{PluginID: pluginID, Version: "1.0.0", SourceInstallationID: sourceID,
			Configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "abcd"})}}})
		if !errors.Is(err, ErrPluginNotInstalled) {
			t.Fatalf("restoring a plugin missing here = %v, want ErrPluginNotInstalled", err)
		}
		if found, _ := installations.ListByPluginID(ctx, pluginID); len(found) != 0 {
			t.Fatalf("a restore installed %d installations", len(found))
		}
	})

	cases := []struct {
		name       string
		configs    []RestoreConfig
		unreadable bool
		wantSaved  bool
	}{
		{name: "valid configuration", configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "abcd"})}}, wantSaved: true},
		{name: "fails validation", configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "x"})}}},
		{name: "another key", configs: []RestoreConfig{{Key: "global", Stored: foreign}}},
		{name: "unreadable key check", configs: []RestoreConfig{{Key: "global", Stored: encode(map[string]any{"api_key": "abcd"})}}, unreadable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pluginID := newPluginID()
			manifest := testPluginManifest(t, pluginID, "1.0.0")
			manifest.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "global",
				JsonSchema: `{"type":"object","properties":{"api_key":{"type":"string","minLength":3}},"required":["api_key"]}`}}
			archivePath := filepath.Join(t.TempDir(), "plugin.zip")
			writePluginArchive(t, archivePath, manifest)
			// Installed by an admin, as a restore never installs.
			installed, err := svc.installer.InstallLocal(ctx, InstallArchiveRequest{ArchivePath: archivePath})
			if err != nil {
				t.Fatal(err)
			}
			in := RestoreInstallationInput{PluginID: pluginID, Version: "1.0.0",
				SourceInstallationID: sourceID, Configs: tc.configs, ConfigUnreadable: tc.unreadable}
			first, err := restore(in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := installations.GetByID(ctx, first.InstallationID)
			if err != nil {
				t.Fatal(err)
			}
			if first.InstallationID != installed.Installation.ID || got.Enabled != installed.Installation.Enabled ||
				(len(first.Written) > 0) != tc.wantSaved || (!tc.wantSaved && len(first.Rejected) == 0) {
				t.Fatalf("restored %+v (enabled %v), want the installation as it was with configuration saved %v", first, got.Enabled, tc.wantSaved)
			}
			if tc.wantSaved {
				var stored []byte
				if err := pool.QueryRow(ctx, `SELECT config_value FROM plugin_runtime_configs WHERE plugin_installation_id = $1 AND config_key = 'global'`, first.InstallationID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if value, err := decodeRuntimeConfigValue(cipher, first.InstallationID, "global", stored); err != nil || value["api_key"] != "abcd" {
					t.Fatalf("configuration under this installation id = %v, %v", value, err)
				}
				if _, err := decodeRuntimeConfigValue(cipher, sourceID, "global", stored); err == nil {
					t.Fatal("configuration still decrypts under the source installation id")
				}
			}
			second, err := restore(in)
			if err != nil || second.Changed() || second.InstallationID != first.InstallationID {
				t.Fatalf("restoring again = %+v, %v; want nothing changed", second, err)
			}
		})
	}
}

// TestRestoreInstallationOnOneConnectionDB: a restore item's transaction
// may hold a pool's only connection, so RestoreInstallation reads the
// installation and its manifest through the transaction, and the archive
// cache never holds its lock while it waits on the pool: a cold-cache
// manifest load on the same pool, already waiting for the connection the
// restore holds, does not stop the restore, and finishes after it.
func TestRestoreInstallationOnOneConnectionDB(t *testing.T) {
	pool := restoreTestPool(t)
	ctx := t.Context()
	cipher, err := secret.New([]byte("plugin-restore-test-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	f := newRestoreFixture(t, pool, cipher)
	pluginID := f.newPluginID()
	manifest := testPluginManifest(t, pluginID, "1.0.0")
	archivePath := filepath.Join(t.TempDir(), "plugin.zip")
	writePluginArchive(t, archivePath, manifest)
	if _, err := f.svc.installer.InstallLocal(ctx, InstallArchiveRequest{ArchivePath: archivePath}); err != nil {
		t.Fatal(err)
	}
	installs, err := f.installations.ListByPluginID(ctx, pluginID)
	if err != nil || len(installs) != 1 {
		t.Fatalf("installations = %v, %v", installs, err)
	}
	inst := installs[0]
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	one, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(one.Close)
	cold := newRestoreFixture(t, one, cipher)
	// The installed files are gone, so every reader rehydrates them from
	// the stored archive.
	binaryPath, err := cold.svc.archiveCache.LocalInstallPath(inst)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(binaryPath)); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{}, 1)
	cold.svc.archiveCache.archives = signalingArchives{archiveStore: cold.svc.archiveCache.archives, reading: waiting}

	tx, err := one.Begin(ctx) // the pool's only connection
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	loaded := make(chan error, 1)
	go func() {
		_, err := cold.svc.archiveCache.Manifest(ctx, inst)
		loaded <- err
	}()
	select {
	case <-waiting: // reading the stored archive, on the pool
	case <-time.After(time.Minute):
		t.Fatal("the cold-cache load never read the stored archive")
	}
	restored := make(chan error, 1)
	go func() {
		_, _, err := cold.svc.RestoreInstallation(ctx, tx, RestoreInstallationInput{PluginID: pluginID, Version: "1.0.0", SourceInstallationID: 1})
		if err == nil {
			err = tx.Commit(ctx)
		}
		restored <- err
	}()
	for name, done := range map[string]chan error{"RestoreInstallation": restored, "the cold-cache load": loaded} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(time.Minute):
			t.Fatalf("%s waited on the pool the restore's transaction holds", name)
		}
	}
}

// signalingArchives reports each archive read as it starts.
type signalingArchives struct {
	archiveStore
	reading chan struct{}
}

func (s signalingArchives) GetArchive(ctx context.Context, installationID int) (*InstallationArchive, error) {
	select {
	case s.reading <- struct{}{}:
	default:
	}
	return s.archiveStore.GetArchive(ctx, installationID)
}

// restoreFixture is TestRestoreInstallationDB's service over pool and its
// helpers: restore runs one RestoreInstallation in its own transaction, and
// newPluginID names a plugin removed when the test ends.
type restoreFixture struct {
	installations *InstallationStore
	svc           *Service
	restore       func(RestoreInstallationInput) (RestoreInstallationResult, error)
	newPluginID   func() string
}

func newRestoreFixture(t *testing.T, pool *pgxpool.Pool, cipher *secret.Cipher) restoreFixture {
	t.Helper()
	ctx := t.Context()
	installations := NewInstallationStore(pool)
	svc := NewService(nil, installations, NewRuntimeConfigStore(pool, cipher), nil,
		NewInstaller(installations, InstallerOptions{BaseDir: t.TempDir()}), nil)
	restore := func(in RestoreInstallationInput) (RestoreInstallationResult, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		result, after, err := svc.RestoreInstallation(ctx, tx, in)
		if err != nil {
			return result, err
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after(ctx)
		return result, nil
	}
	newPluginID := func() string {
		suffix := make([]byte, 4)
		_, _ = rand.Read(suffix)
		id := "test.restore" + hex.EncodeToString(suffix)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE plugin_id = $1`, id)
		})
		return id
	}

	return restoreFixture{installations: installations, svc: svc, restore: restore, newPluginID: newPluginID}
}
