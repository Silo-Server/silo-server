package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

func TestSelectivePluginRestorePreviewsOwnedConfigDB(t *testing.T) {
	f := newSelectiveFixture(t)
	ctx := t.Context()
	installations := plugins.NewInstallationStore(f.pool, f.svc.opts.Cipher)
	configs := plugins.NewRuntimeConfigStore(f.pool, f.svc.opts.Cipher)
	pluginService := plugins.NewService(nil, installations, configs, nil, nil, nil)
	f.svc.SetRestoreTargets(RestoreTargets{
		Plugins: handlers.NewPluginHandler(nil, installations, configs, pluginService, nil, nil, nil, nil, nil),
	})
	installation := seedSelectivePlugin(t, installations)
	const key = "connection"
	const ownedPath = "config.connection.endpoint"
	storeConfig := func(t *testing.T, endpoint, label string) {
		t.Helper()
		if err := configs.PutGlobalConfig(t.Context(), installation.ID, key, map[string]any{"endpoint": endpoint, "label": label}); err != nil {
			t.Fatal(err)
		}
	}
	storeConfig(t, "backup endpoint", "backup label")
	backup, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	preview := func(t *testing.T) (*RestorePreview, PreviewItem) {
		t.Helper()
		p, err := f.svc.Preview(t.Context(), backup.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		return p, previewItem(t, p, SectionPlugins, installation.PluginID)
	}
	restore := func(t *testing.T, p *RestorePreview) ReportItem {
		t.Helper()
		selection := map[string][]string{SectionPlugins: {installation.PluginID}}
		report, err := f.restore(t, backup.ID, p.Digest, selection, &testFence{pool: f.pool})
		if err != nil {
			t.Fatal(err)
		}
		return reportItem(t, report, SectionPlugins, installation.PluginID)
	}
	storeConfig(t, "live endpoint", "backup label")
	beforeOwnership, item := preview(t)
	if item.Status != ItemDiffers || !item.Selectable || item.Message != "" {
		t.Fatalf("preview without ownership: %+v", item)
	}
	dbExec(t, f.pool, `INSERT INTO plugin_provision_ownership (plugin_installation_id, config_fields)
		VALUES ($1, '{"connection":["endpoint"]}')`, installation.ID)
	t.Run("owned difference", func(t *testing.T) {
		_, item := preview(t)
		if item.Status != ItemOnThisServer || item.Selectable || item.Reason != ReasonSame || !strings.Contains(item.Message, ownedPath) {
			t.Fatalf("preview of owned difference: %+v", item)
		}
	})
	t.Run("selection made before ownership changed", func(t *testing.T) {
		item := restore(t, beforeOwnership)
		if item.Outcome != OutcomeUnchanged || item.Reason != "" || !strings.Contains(strings.Join(item.Notes, " "), ownedPath) {
			t.Fatalf("report of stale selection: %+v", item)
		}
	})
	for name, live := range map[string]map[string]any{
		"editable neighbor":                 {"endpoint": "live endpoint", "label": "live label"},
		"unowned field missing from backup": {"endpoint": "live endpoint", "label": "backup label", "region": "live only"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := configs.PutGlobalConfig(ctx, installation.ID, key, live); err != nil {
				t.Fatal(err)
			}
			p, item := preview(t)
			if item.Status != ItemDiffers || !item.Selectable || !strings.Contains(item.Message, ownedPath) {
				t.Fatalf("preview of editable neighbor: %+v", item)
			}
			report := restore(t, p)
			if report.Outcome != OutcomeRestored || !strings.Contains(strings.Join(report.Notes, " "), ownedPath) {
				t.Fatalf("report of editable neighbor: %+v", report)
			}
			current, err := configs.ListGlobalConfigs(ctx, installation.ID)
			if err != nil || len(current) != 1 || !plugins.SameConfig(current[0].Value, map[string]any{"endpoint": "live endpoint", "label": "backup label"}) {
				t.Fatalf("config after restoring editable neighbor: %+v, %v", current, err)
			}
			_, item = preview(t)
			if item.Selectable || !strings.Contains(item.Message, ownedPath) {
				t.Fatalf("preview after restoring editable neighbor: %+v", item)
			}
		})
	}
}

func seedSelectivePlugin(t *testing.T, installations *plugins.InstallationStore) *plugins.Installation {
	t.Helper()
	const pluginID = "silo.test.restore-plugin"
	const version = "1.0.0"
	binary := []byte("test plugin")
	checksum := fmt.Sprintf("%x", sha256.Sum256(binary))
	manifest, err := protojson.Marshal(&pluginv1.PluginManifest{
		PluginId: pluginID, Version: version, Checksum: checksum, SiloApiVersion: plugins.DefaultSiloAPIVersion,
		SupportedPlatforms: []*pluginv1.SupportedPlatform{{Os: "linux", Arch: "amd64"}},
		Capabilities:       []*pluginv1.CapabilityDescriptor{{Id: "metadata", Type: "metadata_provider.v1", DisplayName: "Restore fixture"}},
		GlobalConfigSchema: []*pluginv1.ConfigSchema{{
			Key: "connection", JsonSchema: `{"type":"object","properties":{"endpoint":{"type":"string"},"label":{"type":"string"},"region":{"type":"string"}},"additionalProperties":false}`,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range map[string][]byte{"manifest.json": manifest, "plugin": binary} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	installed, err := installations.Create(t.Context(), plugins.CreateInstallationInput{
		PluginID: pluginID, Version: version, InstallPath: filepath.Join(t.TempDir(), "installed", "plugin"),
		Archive: plugins.ArchiveInput{ManifestJSON: manifest, Checksum: checksum, Bytes: archive.Bytes()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return installed
}
