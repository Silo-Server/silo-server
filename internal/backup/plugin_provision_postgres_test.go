package backup

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/Silo-Server/silo-server/migrations"
)

const (
	pluginProvisionMatchedID    = 10001
	pluginProvisionMismatchedID = 10002
	pluginProvisionDeployment   = "provision-restore-test"
	pluginProvisionBackupHold   = "backup-hold"
	pluginProvisionLiveHold     = "live-hold"
)

type pluginProvisionRestoreCase struct {
	name                                    string
	separate, lostKey, older, samePluginIDs bool
}

func TestRestorePluginProvisioningDB(t *testing.T) {
	tools := testTools(t)
	loader := PGLoader{Tools: tools}
	master := []byte("provision-restore-test-key-0123456789abcdef")
	manifestKey := manifestKeyOf(t, master)
	keyCheck, err := secret.KeyCheckValue(master)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := database.MigrationVersions(migrations.FS, "sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []pluginProvisionRestoreCase{
		{name: "same deployment"},
		{name: "authority restriction on older schema", older: true, samePluginIDs: true},
		{name: "lost key", lostKey: true},
		{name: "older separate restore", older: true, separate: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := t.Context()
			source, sourceURL := backuptest.Database(t)
			seedPluginProvisionRestore(t, source, "backup-plugin", "1.0.0")
			dbExec(t, source, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, plugins.ProvisionRestoreHoldKey, pluginProvisionBackupHold)
			dbExec(t, source, `UPDATE access_groups SET max_streams = 0 WHERE is_default`)
			if scenario.older {
				dbExec(t, source, `DROP TABLE plugin_provision_ownership, plugin_provision_status`)
				dbExec(t, source, `ALTER TABLE plugin_auth_bindings DROP COLUMN callback_mode`)
				dbExec(t, source, `DELETE FROM goose_db_version WHERE version_id = ANY($1::bigint[])`,
					[]int64{20261119202657, 20261119202701, 20261119202736})
			}
			validate := ValidateOptions{KeyCheck: keyCheck, ManifestKey: manifestKey, BinaryMigrations: versions}
			src := backupPluginProvisionRestore(t, source, sourceURL, tools, validate)
			if scenario.lostKey {
				other := []byte("unrelated-provision-key-0123456789abcdef")
				otherKeyCheck, err := secret.KeyCheckValue(other)
				if err != nil {
					t.Fatal(err)
				}
				validate.KeyCheck = otherKeyCheck
				validate.ManifestKey = manifestKeyOf(t, other)
				validate.AcceptLostCredentials = true
				validate.AcceptSuperuser = true
			}

			var out bytes.Buffer
			var restoredURL string
			if scenario.separate {
				restoredURL = createDatabase(t, source, sourceURL)
				if _, err := RestoreSeparate(ctx, src, loader, SeparateOptions{
					Into: restoredURL, DatabaseOnly: true, Validate: validate, Out: &out,
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				restoredURL = swapPluginProvisionRestore(t, src, loader, SwapOptions{
					DatabaseOnly: true, Validate: validate, WorkDir: t.TempDir(), JournalKey: manifestKey, Out: &out,
				}, scenario)
			}
			restored, err := pgxpool.New(ctx, restoredURL)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if scenario.older {
				if err := database.MigrateOnly(ctx, restored, migrations.FS, "sql"); err != nil {
					t.Fatal(err)
				}
			}
			hold := checkPluginProvisionRestoreHold(t, restored, &out)
			checkPluginProvisionRestoredState(t, restored, scenario)
			acknowledgePluginProvisionRestoreHold(t, restored, hold)
		})
	}
}

func seedPluginProvisionRestore(t *testing.T, pool *pgxpool.Pool, pluginID, pin string) {
	t.Helper()
	dbExec(t, pool, `INSERT INTO plugin_installations (id, plugin_id, version, install_path, enabled)
		VALUES ($1, 'silo.auth.oidc', $3, '/plugins/oidc', true), ($2, $4, $3, '/plugins/other', true)`,
		pluginProvisionMatchedID, pluginProvisionMismatchedID, pin, pluginID)
	dbExec(t, pool, `INSERT INTO plugin_provision_ownership (plugin_installation_id, version_pin, config_fields, binding_fields)
		VALUES ($1, $3, '{"connection":["client_secret"]}', '{"oidc":["enabled"]}'), ($2, $3, '{}', '{}')`,
		pluginProvisionMatchedID, pluginProvisionMismatchedID, pin)
	dbExec(t, pool, `INSERT INTO plugin_auth_bindings (id, plugin_installation_id, capability_id, auto_provision, callback_mode)
		VALUES (10001, $1, 'oidc', true, 'stable'), (10002, $2, 'other', false, 'stable')`,
		pluginProvisionMatchedID, pluginProvisionMismatchedID)
	dbExec(t, pool, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, serveridentity.Key, pluginProvisionDeployment)
	dbExec(t, pool, `INSERT INTO plugin_provision_status (plugin_id, version, state, detail) VALUES ('silo.auth.oidc', $1, 'pending', 'stale')`, pin)
}

func backupPluginProvisionRestore(t *testing.T, pool *pgxpool.Pool, dbURL string, tools Tools, validate ValidateOptions) *Source {
	t.Helper()
	root := t.TempDir()
	store, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Pool: pool, DatabaseURL: dbURL, Destination: NewStoreDestination(store),
		Locker: PostgresLocker{Pool: pool}, KeyCheck: validate.KeyCheck, ManifestKey: validate.ManifestKey,
		ServerID:  func(_ context.Context) (string, error) { return pluginProvisionDeployment, nil },
		FindTools: func(_ context.Context) (Tools, error) { return tools, nil },
	})
	result, err := backupUnderLock(t, svc, KindBeforeUpgrade)
	if err != nil {
		t.Fatal(err)
	}
	src := openTestSource(t, filepath.Join(root, "backups", pluginProvisionDeployment, result.ID))
	if !slices.Contains(src.Manifest.ExcludedTableData, tablePluginProvisionStatus) {
		t.Fatal("backup did not exclude transient provisioning status")
	}
	return src
}

func swapPluginProvisionRestore(t *testing.T, src *Source, loader PGLoader, opts SwapOptions, scenario pluginProvisionRestoreCase) string {
	t.Helper()
	ctx := t.Context()
	destination, dstURL := backuptest.Database(t)
	pluginID := "live-plugin"
	if scenario.samePluginIDs {
		pluginID = "backup-plugin"
	}
	seedPluginProvisionRestore(t, destination, pluginID, "2.0.0")
	if scenario.lostKey {
		dbExec(t, destination, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, plugins.ProvisionRestoreHoldKey, pluginProvisionLiveHold)
	}
	dbExec(t, destination, `UPDATE access_groups SET max_streams = 1 WHERE is_default`)
	destination.Close()
	ctl, err := ConnectControl(ctx, dstURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctl.Close(ctx) }()
	opts.LiveURL = dstURL
	var previous string
	opts.Journaled = func(stamp string) { previous = "silo_before_restore_" + stamp }
	if err := RestoreSwap(ctx, src, loader, ctl, opts); err != nil {
		t.Fatal(err)
	}
	// The swap retains the old database under a name shared across tests
	// within the same second. This fixture is finished with it.
	if err := ctl.Drop(ctx, previous); err != nil {
		t.Fatal(err)
	}
	return dstURL
}

func checkPluginProvisionRestoredState(t *testing.T, restored *pgxpool.Pool, scenario pluginProvisionRestoreCase) {
	t.Helper()
	ctx := t.Context()
	var owners, status, bindings int
	var mismatchEnabled bool
	err := restored.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM plugin_provision_ownership),
		(SELECT count(*) FROM plugin_provision_status),
		(SELECT count(*) FROM plugin_auth_bindings),
		(SELECT enabled FROM plugin_installations WHERE id = $1)`, pluginProvisionMismatchedID).Scan(&owners, &status, &bindings, &mismatchEnabled)
	if err != nil || status != 0 || mismatchEnabled != scenario.samePluginIDs {
		t.Fatalf("transient status = %d, mismatched release enabled = %v, error = %v", status, mismatchEnabled, err)
	}
	if scenario.separate || scenario.lostKey {
		if owners != 0 || bindings != 0 {
			t.Fatalf("foreign restore has %d ownership rows and %d bindings", owners, bindings)
		}
		return
	}
	expectedMatches := 1
	if scenario.samePluginIDs {
		expectedMatches = 2
	}
	var pin, version, callback string
	var configOwned, bindingOwned, canProvision bool
	err = restored.QueryRow(ctx, `SELECT o.version_pin, i.version, b.callback_mode,
		o.config_fields = '{"connection":["client_secret"]}', o.binding_fields = '{"oidc":["enabled"]}', b.auto_provision
		FROM plugin_provision_ownership o
		JOIN plugin_installations i ON i.id = o.plugin_installation_id
		JOIN plugin_auth_bindings b ON b.plugin_installation_id = i.id
		WHERE i.id = $1`, pluginProvisionMatchedID).Scan(&pin, &version, &callback, &configOwned, &bindingOwned, &canProvision)
	if err != nil || owners != expectedMatches || bindings != expectedMatches || pin != "2.0.0" || version != pin || callback != "stable" || !configOwned || !bindingOwned || canProvision {
		t.Fatalf("matched restore: pin %q, version %q, callback %q, config %t, binding %t, auto-provision %t, owners %d, bindings %d, error %v",
			pin, version, callback, configOwned, bindingOwned, canProvision, owners, bindings, err)
	}
}

func checkPluginProvisionRestoreHold(t *testing.T, restored *pgxpool.Pool, out *bytes.Buffer) string {
	t.Helper()
	ctx := t.Context()
	hold, err := plugins.ProvisionRestoreHold(ctx, restored)
	if err != nil || hold == "" || hold == pluginProvisionBackupHold || hold == pluginProvisionLiveHold || !bytes.Contains(out.Bytes(), []byte(hold)) {
		t.Fatalf("restored hold = %q (%v), want a fresh ID in the restore report:\n%s", hold, err, out.String())
	}
	for _, id := range []string{pluginProvisionBackupHold, pluginProvisionLiveHold} {
		if err := plugins.AcknowledgeProvisionRestoreHold(ctx, restored, id); !errors.Is(err, plugins.ErrProvisionRestoreHoldChanged) {
			t.Fatalf("acknowledging stale hold %q: %v", id, err)
		}
	}
	return hold
}

func acknowledgePluginProvisionRestoreHold(t *testing.T, restored *pgxpool.Pool, hold string) {
	t.Helper()
	ctx := t.Context()
	if err := plugins.AcknowledgeProvisionRestoreHold(ctx, restored, hold); err != nil {
		t.Fatal(err)
	}
	tx, err := restored.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if id, err := plugins.ProvisionRestoreHold(ctx, tx); err != nil || id != "" {
		t.Fatalf("hold after acknowledgement = %q (%v)", id, err)
	}
}
