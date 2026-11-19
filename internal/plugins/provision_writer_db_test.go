package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
)

//nolint:funlen,gocyclo // Each assertion follows a step in one writer's transaction sequence.
func TestProvisionWriterAtomicOfflineDB(t *testing.T) {
	original := builtinGuardTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cfg := original.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	repositories := NewRepositoryStore(pool)
	if _, err := repositories.ReconcileManaged(ctx); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalogService(repositories, CatalogServiceOptions{})
	repo, err := catalog.OfficialRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cipher := instanceStateTestCipher(t)
	installations := NewInstallationStore(pool, cipher)
	configs := NewRuntimeConfigStore(pool, cipher)
	installer := NewInstaller(installations, InstallerOptions{BaseDir: t.TempDir()})
	first := seedTrustedProvisionInstallation(t, pool, installations, repo.ID, tmdbID)
	second := seedTrustedProvisionInstallation(t, pool, installations, repo.ID, pluginIDTVDB)

	doc := &ProvisionDocument{APIVersion: 1, Plugins: map[string]ProvisionPlugin{}}
	for _, id := range []string{first.PluginID, second.PluginID} {
		doc.Plugins[id] = ProvisionPlugin{
			Version: "1.2.3",
			Config:  map[string]map[string]any{"connection": {"label": "configured"}},
			AuthBinding: &ProvisionAuthBinding{
				CapabilityID: "login", Enabled: new(true),
			},
		}
	}
	writer := NewProvisionWriter(pool, repositories, installations, installer, catalog, configs, nil, doc, nil)
	t.Cleanup(writer.Close)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_provision_status WHERE plugin_id = ANY($1::text[])`, []string{first.PluginID, second.PluginID}); err != nil {
			t.Error(err)
		}
	})
	if err := writer.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	statuses, err := ListProvisionStatus(ctx, pool)
	if err != nil || len(statuses) != 2 || statuses[0].State != "refused" || statuses[1].State != "refused" {
		t.Fatalf("conflicting declaration status: %#v, %v", statuses, err)
	}
	for _, installation := range []*Installation{first, second} {
		rows, err := configs.ListGlobalConfigs(ctx, installation.ID)
		if err != nil || len(rows) != 0 {
			t.Fatalf("failed declaration persisted config for %s: %d rows, %v", installation.PluginID, len(rows), err)
		}
		owned, err := installations.GetProvisionOwnership(ctx, installation.ID)
		if err != nil || owned.Managed {
			t.Fatalf("failed declaration persisted ownership for %s: %v", installation.PluginID, err)
		}
		_, err = configs.GetAuthBinding(ctx, installation.ID, "login")
		if !errors.Is(err, ErrAuthBindingNotFound) {
			t.Fatalf("failed declaration persisted binding for %s: %v", installation.PluginID, err)
		}
	}

	contender := NewProvisionWriter(pool, repositories, installations, installer, catalog, configs, nil, doc, nil)
	t.Cleanup(contender.Close)
	if err := contender.StartOffline(ctx); err != nil || contender.conn != nil || len(contender.LocalStatus()) != 2 {
		t.Fatalf("second writer acquired the lock or lost conflict status: %v", err)
	}

	// Retry with a valid declaration. This is the same transaction path the
	// background writer uses after it resolves a package.
	doc.Plugins[second.PluginID].AuthBinding.Enabled = new(false)
	if retry, err := writer.apply(ctx, true); retry || err != nil {
		t.Fatalf("applying corrected document, retry=%v: %v", retry, err)
	}
	for _, installation := range []*Installation{first, second} {
		got, err := installations.GetByID(ctx, installation.ID)
		if err != nil || got.RuntimeGeneration != installation.RuntimeGeneration+1 {
			t.Fatalf("expected one runtime generation change for %s: %v, %v", installation.PluginID, got, err)
		}
	}
	binding, err := configs.GetAuthBinding(ctx, first.ID, "login")
	if err != nil || !binding.Enabled || !binding.AutoProvision || binding.CallbackMode != AuthCallbackStable {
		t.Fatalf("binding defaults: %v, %v", binding, err)
	}
	before, err := configs.ListGlobalConfigs(ctx, first.ID)
	if err != nil || len(before) != 1 {
		t.Fatalf("read applied config: %v", err)
	}
	if retry, err := writer.apply(ctx, true); retry || err != nil {
		t.Fatalf("reapplying identical document, retry=%v: %v", retry, err)
	}
	after, err := configs.ListGlobalConfigs(ctx, first.ID)
	if err != nil || len(after) != 1 || !before[0].UpdatedAt.Equal(after[0].UpdatedAt) {
		t.Fatal("reapplying identical input changed the saved config row")
	}
	unchanged, err := installations.GetByID(ctx, first.ID)
	if err != nil || unchanged.RuntimeGeneration != first.RuntimeGeneration+1 {
		t.Fatalf("reapplying identical input changed the generation: %v, %v", unchanged, err)
	}

	// An omitted entry is still passed to a replacement process. A candidate
	// with an incompatible schema must fail even when the document leaves
	// that saved entry out.
	replacement := packageErrorTestManifest(first.PluginID, "2.0.0")
	replacement.GlobalConfigSchema = []*pluginv1.ConfigSchema{{
		Key: "connection", JsonSchema: `{"type":"object","properties":{"label":{"type":"integer"}}}`,
	}}
	item := preparedProvision{pluginID: first.PluginID, installed: unchanged,
		pkg: &PreparedProvisionPackage{Manifest: replacement, existing: unchanged, stage: &provisionPackageStage{}},
	}
	_, err = writer.planConfigValues(ctx, pool, item, first.ID)
	if refusal, ok := errors.AsType[*provisionRefusalError](err); !ok || refusal.detail != "config.connection.label does not satisfy the plugin configuration schema." {
		t.Fatalf("replacement config validation: %v", err)
	}

	firstDesired := doc.Plugins[first.PluginID]
	firstDesired.Version = ""
	doc.Plugins[first.PluginID] = firstDesired
	if retry, err := writer.apply(ctx, true); retry || err != nil {
		t.Fatalf("releasing pin, retry=%v: %v", retry, err)
	}
	if err := installations.Update(ctx, first.ID, UpdateInstallationInput{
		AvailableVersion: new("2.0.0"),
		UpdateFailure:    &UpdateFailure{Version: "2.0.0", Reason: "stored hint", FailedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	firstDesired.Version = unchanged.Version
	doc.Plugins[first.PluginID] = firstDesired
	if retry, err := writer.apply(ctx, true); retry || err != nil {
		t.Fatalf("pinning installed version, retry=%v: %v", retry, err)
	}
	pinned, err := installations.GetByID(ctx, first.ID)
	if err != nil || pinned.AvailableVersion != nil || pinned.UpdateFailure != nil || pinned.RuntimeGeneration != unchanged.RuntimeGeneration {
		t.Fatalf("pin left obsolete update metadata or bumped generation: %v, %v", pinned, err)
	}

	for _, declared := range []map[string]ProvisionPlugin{{}, {first.PluginID: {Version: "2.0.0"}}} {
		conflict := NewProvisionWriter(pool, repositories, installations, installer, catalog, configs, nil,
			&ProvisionDocument{APIVersion: 1, Plugins: declared}, nil)
		if err := conflict.StartOffline(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conflict.Close)
		configs.SetProvisionWriter(conflict)
		localStatus, err := configs.ListProvisionStatus(ctx)
		if err != nil || len(localStatus) != 2 || localStatus[0].State != "refused" || localStatus[1].State != "refused" {
			t.Fatalf("release blocked by competing writer: %#v, %v", localStatus, err)
		}
	}
	committedStatus, err := ListProvisionStatus(ctx, pool)
	if err != nil || len(committedStatus) != 2 || committedStatus[0].State != "applied" || committedStatus[1].State != "applied" {
		t.Fatalf("empty declaration modified committed status: %#v, %v", committedStatus, err)
	}

	// Only an explicit empty declaration releases ownership.
	doc.Plugins = map[string]ProvisionPlugin{}
	if retry, err := writer.apply(ctx, true); retry || err != nil {
		t.Fatalf("releasing ownership, retry=%v: %v", retry, err)
	}
	owned, err := installations.GetProvisionOwnership(ctx, first.ID)
	if err != nil || owned.Managed {
		t.Fatalf("empty declaration did not release ownership: %v, %v", owned, err)
	}
	afterRelease, err := configs.ListGlobalConfigs(ctx, first.ID)
	if err != nil || len(afterRelease) != 1 || !before[0].UpdatedAt.Equal(afterRelease[0].UpdatedAt) {
		t.Fatal("releasing ownership rewrote saved configuration")
	}
	statuses, err = ListProvisionStatus(ctx, pool)
	if err != nil || len(statuses) != 0 {
		t.Fatalf("empty declaration left obsolete status: %#v, %v", statuses, err)
	}

	assertProvisionPreflightRetriesStaleArchive(t, ctx, writer, first.ID)
}

func assertProvisionPreflightRetriesStaleArchive(t *testing.T, ctx context.Context, writer *ProvisionWriter, installationID int) {
	t.Helper()
	current, err := writer.installations.GetByID(ctx, installationID)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := writer.catalog.PrepareProvisionPackage(ctx, writer.installer, ProvisionPackageRequest{
		PluginID: current.PluginID, Existing: current, Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.pool.Exec(ctx, `UPDATE plugin_archives SET updated_at = updated_at + INTERVAL '1 second' WHERE plugin_installation_id = $1`, installationID); err != nil {
		t.Fatal(err)
	}
	desired := ProvisionPlugin{Config: map[string]map[string]any{"connection": {"label": 5}}}
	writer.document.Plugins[current.PluginID] = desired
	item := preparedProvision{pluginID: current.PluginID, installed: current, pkg: pkg, desired: desired}
	if err := writer.preflightValues(ctx, &item); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("stale archive preflight should retry without validating the desired fields: %v", err)
	}
}

func seedTrustedProvisionInstallation(t *testing.T, pool *pgxpool.Pool, store *InstallationStore, repository int, pluginID string) *Installation {
	t.Helper()
	source, ok := officialPluginRepository(pluginID)
	if !ok {
		t.Fatalf("fixture plugin %s is outside the official catalog", pluginID)
	}
	binary := []byte("provisioning archive fixture")
	manifest := packageErrorTestManifest(pluginID, "1.2.3")
	manifest.Checksum = sha256Hex(binary)
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{{
		Id: "login", Type: "auth_provider.v1", DisplayName: "Test Login", AuthModes: []string{"oauth2"},
	}}
	manifest.GlobalConfigSchema = []*pluginv1.ConfigSchema{{
		Key: "connection", JsonSchema: `{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false}`,
	}}
	manifestJSON, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := buildBinaryPluginArchive(manifestJSON, binary)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := CapabilityRecordsFromManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	installation, err := store.Create(t.Context(), CreateInstallationInput{
		RepositoryID: repository, PluginID: pluginID, Version: manifest.Version,
		InstallPath: "/nonexistent/provision-test/" + pluginID + "/plugin", Enabled: true, Capabilities: caps,
		Signature: Signature{State: SignatureStateVerified, SourceRepo: &source, VerifiedAt: new(time.Now())},
		Archive:   ArchiveInput{ManifestJSON: manifestJSON, Checksum: manifest.Checksum, Bytes: archive},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installation.ID); err != nil {
			t.Error(err)
		}
	})
	return installation
}
