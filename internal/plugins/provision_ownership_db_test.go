package plugins

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestProvisionOwnershipGuardsDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	cipher := instanceStateTestCipher(t)
	id := seedResidentQueryInstallation(t, pool, false)
	inst := NewInstallationStore(pool, cipher)
	config := NewRuntimeConfigStore(pool, cipher)

	saveOwnership := func(ownership ProvisionOwnership) {
		t.Helper()
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := lockPluginInstallation(ctx, tx, id); err != nil {
				return err
			}
			return PutProvisionOwnership(ctx, tx, id, ownership)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	current := func() *RuntimeConfig {
		t.Helper()
		rows, err := config.ListGlobalConfigs(ctx, id)
		if err != nil || len(rows) != 1 {
			t.Fatalf("read %d config rows, error %v", len(rows), err)
		}
		return rows[0]
	}
	requireManaged := func(err error, field string) {
		t.Helper()
		managed, ok := errors.AsType[*ManagedByEnvironmentError](err)
		if !ok || managed.Field != field {
			t.Fatalf("got %v, want managed error for %s", err, field)
		}
	}

	if err := config.PutGlobalConfig(ctx, id, "app", map[string]any{"secret": "saved", "scopes": "original", "old": "removed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'identity', '{"auth_modes":["oauth2"]}')`, id); err != nil {
		t.Fatal(err)
	}

	binding := AuthBinding{InstallationID: id, CapabilityID: "identity", Enabled: true}
	if err := config.UpsertAuthBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	saveOwnership(ProvisionOwnership{Managed: true, VersionPin: "0",
		ConfigFields:  map[string][]string{"app": {"absent", "secret"}, "seed": {"token"}},
		BindingFields: map[string][]string{"identity": {"enabled"}},
	})
	if err := config.PutGlobalConfig(ctx, id, "app", map[string]any{"secret": "stale", "absent": "stale", "scopes": "runtime"}); err != nil {
		t.Fatal(err)
	}
	stored := current()
	if !SameConfig(stored.Value, map[string]any{"secret": "saved", "scopes": "runtime"}) {
		t.Fatal("runtime update did not preserve managed values and replace unowned fields")
	}
	if err := config.PutGlobalConfig(ctx, id, "app", map[string]any{"scopes": "runtime"}); err != nil {
		t.Fatal(err)
	}
	if !current().UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatal("identical runtime update rewrote the row")
	}
	_, err := config.CompareAndSwapGlobalConfig(ctx, id, "seed", map[string]any{"token": "legacy seed"}, nil)
	requireManaged(err, "config.seed.token")
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return putConfigInTx(ctx, tx, config, id, "app", map[string]any{"secret": "backup"})
	})
	requireManaged(err, "config.app.secret")
	_, err = config.CompareAndSwapGlobalConfig(ctx, id, "app", map[string]any{"secret": "replace"}, &stored.UpdatedAt)
	requireManaged(err, "config.app.secret")
	_, err = config.compareAndSwapGlobalConfig(ctx, id, "app", stored.Value, &stored.UpdatedAt, nil, map[string]struct{}{"absent": {}}, nil)
	requireManaged(err, "config.app.absent")
	saved, err := config.CompareAndSwapGlobalConfig(ctx, id, "app", map[string]any{"secret": "saved", "scopes": "admin"}, &stored.UpdatedAt)
	if err != nil || !saved {
		t.Fatalf("adjacent admin write saved %v, error %v", saved, err)
	}

	binding.DisplayOrder = 2
	if err := config.UpsertAuthBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	binding.Enabled = false
	requireManaged(config.UpsertAuthBinding(ctx, binding), "auth_binding.enabled")
	requireManaged(inst.Delete(ctx, id), "installation")
	requireManaged(inst.Update(ctx, id, UpdateInstallationInput{AvailableVersion: new("1")}), "version")
	requireManaged(inst.Update(ctx, id, UpdateInstallationInput{UpdatePolicy: new("auto")}), "update_policy")

	saveOwnership(ProvisionOwnership{})
	binding.Enabled = false
	if err := config.UpsertAuthBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := inst.Update(ctx, id, UpdateInstallationInput{UpdatePolicy: new("auto")}); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionedReplacementValidatesStoredConfigDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	cipher := instanceStateTestCipher(t)
	installations := NewInstallationStore(pool, cipher)
	config := NewRuntimeConfigStore(pool, cipher)
	installed := seedVerifiedReplacementInstallation(t, pool, installations)
	id := installed.ID
	previous := testPluginManifest(t, installed.PluginID, installed.Version)
	previous.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "app",
		JsonSchema: `{"type":"object","properties":{"secret":{"type":"string","writeOnly":true},"scopes":{"type":"string"}}}`}}
	previousJSON, err := protojson.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := installations.SaveArchive(ctx, id, previousJSON, previous.Checksum, []byte("installed fixture")); err != nil {
		t.Fatal(err)
	}

	if err := config.PutGlobalConfig(ctx, id, "app", map[string]any{"secret": "saved", "scopes": "existing", "cursor": "private"}); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := lockPluginInstallation(ctx, tx, id); err != nil {
			return err
		}
		return PutProvisionOwnership(ctx, tx, id, ProvisionOwnership{Managed: true, ConfigFields: map[string][]string{"app": {"secret"}}})
	})
	if err != nil {
		t.Fatal(err)
	}

	const updatedVersion = "1.0.0"
	manifest := testPluginManifest(t, installed.PluginID, updatedVersion)
	update := func(schema string) error {
		t.Helper()
		manifest.GlobalConfigSchema = nil
		if schema != "" {
			manifest.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "app", JsonSchema: schema}}
		}
		manifestJSON, err := protojson.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		return installations.Update(ctx, id, UpdateInstallationInput{
			IfVersion: &installed.Version, Version: new(updatedVersion), SetRepository: true,
			RepositoryID: installed.RepositoryID, Signature: &installed.Signature,
			Archive: &ArchiveInput{ManifestJSON: manifestJSON, Checksum: manifest.Checksum, Bytes: []byte("test package")},
		})
	}
	for _, schema := range []string{"", `{"type":"object","properties":{"secret":{"type":"string"}}}`} {
		err := update(schema)
		if _, ok := AsPackageError(err); !ok {
			t.Fatalf("update that removes credential protection: got %v, want package refusal", err)
		}
	}

	err = update(`{"type":"object","properties":{"secret":{"type":"string","writeOnly":true},"scopes":{"type":"array"}},"additionalProperties":false}`)
	if managed, ok := errors.AsType[*ManagedByEnvironmentError](err); !ok || managed.Field != "config.app" {
		t.Fatalf("incompatible unowned field in managed entry: got %v, want managed config rejection", err)
	}
	got, err := installations.GetByID(ctx, id)
	if err != nil || got.Version != installed.Version {
		t.Fatalf("refused update changed version: %+v, error %v", got, err)
	}
	if err := update(`{"type":"object","properties":{"secret":{"type":"string","writeOnly":true},"scopes":{"type":"string"}},"additionalProperties":false}`); err != nil {
		t.Fatalf("compatible update: %v", err)
	}
	got, err = installations.GetByID(ctx, id)
	if err != nil || got.Version != updatedVersion {
		t.Fatalf("compatible update did not change version: %+v, error %v", got, err)
	}

	configs, err := config.ListGlobalConfigs(ctx, id)
	if err != nil || len(configs) != 1 || configs[0].Value["cursor"] != "private" {
		t.Fatalf("private state after update: %v, %v", configs, err)
	}
	validated := false
	changed, err := config.compareAndSwapGlobalConfig(ctx, id, "app", configs[0].Value, &configs[0].UpdatedAt,
		installed, nil, func() error { validated = true; return nil })
	if changed || validated || !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("stale package config write: changed %t, validated %t, error %v", changed, validated, err)
	}

}

func TestProvisionedReplacementValidatesOwnedCallbackDB(t *testing.T) {
	ctx := t.Context()
	pool := builtinGuardTestPool(t)
	installations := NewInstallationStore(pool)
	configs := NewRuntimeConfigStore(pool)
	installed := seedVerifiedReplacementInstallation(t, pool, installations)
	const capabilityID = "login"
	update := func(version, mode string) error {
		t.Helper()
		manifest := testPluginManifest(t, installed.PluginID, version)
		manifest.Capabilities = []*pluginv1.CapabilityDescriptor{{
			Id: capabilityID, Type: "auth_provider.v1", DisplayName: "Test Login", AuthModes: []string{mode},
		}}
		manifestJSON, err := protojson.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		caps, err := CapabilityRecordsFromManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		return installations.Update(ctx, installed.ID, UpdateInstallationInput{
			IfVersion: &installed.Version, Version: new(version), SetRepository: true,
			RepositoryID: installed.RepositoryID, Signature: &installed.Signature, Capabilities: caps,
			Archive: &ArchiveInput{ManifestJSON: manifestJSON, Checksum: manifest.Checksum, Bytes: []byte("test package")},
		})
	}
	if err := update(installed.Version, "oauth2"); err != nil {
		t.Fatal(err)
	}
	if err := configs.UpsertAuthBinding(ctx, AuthBinding{
		InstallationID: installed.ID, CapabilityID: capabilityID, Enabled: true, CallbackMode: AuthCallbackInstallation,
	}); err != nil {
		t.Fatal(err)
	}
	setOwnedFields := func(fields ...string) {
		t.Helper()
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := lockPluginInstallation(ctx, tx, installed.ID); err != nil {
				return err
			}
			return PutProvisionOwnership(ctx, tx, installed.ID, ProvisionOwnership{
				Managed: true, BindingFields: map[string][]string{capabilityID: fields},
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setOwnedFields("callback_mode", "enabled")
	err := update("1.0.0", "password")
	if managed, ok := errors.AsType[*ManagedByEnvironmentError](err); !ok || managed.Field != "auth_binding.callback_mode" {
		t.Fatalf("replacement changed the authentication mode of an owned OAuth callback: %v", err)
	}
	setOwnedFields("enabled")
	if err := update("1.0.0", "password"); err != nil {
		t.Fatalf("replacement refused the default callback mode for a password binding: %v", err)
	}
}

func seedVerifiedReplacementInstallation(t *testing.T, pool *pgxpool.Pool, installations *InstallationStore) *Installation {
	t.Helper()
	ctx := t.Context()
	id := seedResidentQueryInstallation(t, pool, true)
	installed, err := installations.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var repositoryID int
	err = pool.QueryRow(ctx, `INSERT INTO plugin_repositories (url, display_name) VALUES ($1, 'Provision test') RETURNING id`,
		"https://plugins.example.test/"+installed.PluginID).Scan(&repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_repositories WHERE id = $1`, repositoryID); err != nil {
			t.Errorf("delete test repository %d: %v", repositoryID, err)
		}
	})
	signature := Signature{State: SignatureStateVerified, SourceRepo: new("https://github.com/example/plugin")}
	if err := installations.Update(ctx, id, UpdateInstallationInput{SetRepository: true, RepositoryID: &repositoryID, Signature: &signature}); err != nil {
		t.Fatal(err)
	}
	installed, err = installations.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}

func TestReenableRejectsIneligibleStableBindingDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	id := seedResidentQueryInstallation(t, pool, false)
	installations := NewInstallationStore(pool)
	_, err := pool.Exec(ctx, `INSERT INTO plugin_auth_bindings
		(plugin_installation_id, capability_id, enabled, callback_mode) VALUES ($1, 'login', true, 'stable')`, id)
	if err != nil {
		t.Fatal(err)
	}
	assertIneligible := func() {
		t.Helper()
		err := installations.Update(ctx, id, UpdateInstallationInput{Enabled: new(true)})
		if !errors.Is(err, ErrAuthBindingIneligible) {
			t.Fatalf("enable with ineligible binding: %v", err)
		}
		got, err := installations.GetByID(ctx, id)
		if err != nil || got.Enabled {
			t.Fatalf("refused enable changed installation: %+v, error %v", got, err)
		}
	}
	assertIneligible()
	_, err = pool.Exec(ctx, `INSERT INTO plugin_capabilities
		(plugin_installation_id, capability_type, capability_id, metadata)
		VALUES ($1, 'auth_provider.v1', 'login', '{"auth_modes":["password"]}')`, id)
	if err != nil {
		t.Fatal(err)
	}
	assertIneligible()
}
