package plugins

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

// The host reads supports_seasons and reports_download_progress from the
// capability metadata stored from the manifest, the way install records it.
func TestRequestRouterDescriptorReadsStoredManifestFlag(t *testing.T) {
	records, err := CapabilityRecordsFromManifest(&pluginv1.PluginManifest{
		Capabilities: []*pluginv1.CapabilityDescriptor{
			{Type: capability.RequestRouter, Id: "arr", RequestRouter: &pluginv1.RequestRouterDescriptor{SupportsSeasons: true}},
			{Type: capability.RequestRouter, Id: "legacy"},
			{Type: capability.RequestRouter, Id: "progress", RequestRouter: &pluginv1.RequestRouterDescriptor{SupportsSeasons: true, ReportsDownloadProgress: true}},
		},
	})
	if err != nil {
		t.Fatalf("CapabilityRecordsFromManifest: %v", err)
	}
	store := &fakeServiceInstallationStore{}
	for i := range records {
		store.listCapabilities = append(store.listCapabilities, &records[i])
	}
	service := &Service{installations: store}

	for _, tc := range []struct {
		capabilityID string
		want         bool
		wantProgress bool
	}{
		{"arr", true, false},      // built before reports_download_progress existed
		{"legacy", false, false},  // built before the descriptor existed
		{"missing", false, false}, // not declared at all
		{"progress", true, true},
	} {
		got, err := service.RequestRouterDescriptor(context.Background(), 5, tc.capabilityID)
		if err != nil {
			t.Fatalf("%s: RequestRouterDescriptor: %v", tc.capabilityID, err)
		}
		if got.GetSupportsSeasons() != tc.want {
			t.Errorf("%s: supports_seasons = %v, want %v", tc.capabilityID, got.GetSupportsSeasons(), tc.want)
		}
		if got.GetReportsDownloadProgress() != tc.wantProgress {
			t.Errorf("%s: reports_download_progress = %v, want %v", tc.capabilityID, got.GetReportsDownloadProgress(), tc.wantProgress)
		}
	}
}

// Through the database: the flag survives the JSONB capability metadata, and
// replacing an installation's capabilities in place (a plugin upgraded from a
// build without the flag) turns it on.
func TestRequestRouterDescriptorFromStoredCapabilities(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewInstallationStore(pool)

	manifestCapabilities := func(supportsSeasons bool) []Capability {
		descriptor := &pluginv1.CapabilityDescriptor{Type: capability.RequestRouter, Id: "arr"}
		if supportsSeasons {
			descriptor.RequestRouter = &pluginv1.RequestRouterDescriptor{SupportsSeasons: true}
		}
		records, err := CapabilityRecordsFromManifest(&pluginv1.PluginManifest{Capabilities: []*pluginv1.CapabilityDescriptor{descriptor}})
		if err != nil {
			t.Fatalf("CapabilityRecordsFromManifest: %v", err)
		}
		return records
	}
	installation, err := store.Create(ctx, CreateInstallationInput{
		PluginID:     "test.requests.seasons" + time.Now().UTC().Format("-20060102150405.000000000"),
		Version:      "0.1.6",
		InstallPath:  "/nonexistent/requests-seasons-test",
		UpdatePolicy: "manual",
		Capabilities: manifestCapabilities(false),
	})
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installation.ID)
	})
	service := &Service{installations: store}

	before, err := service.RequestRouterDescriptor(ctx, installation.ID, "arr")
	if err != nil {
		t.Fatalf("RequestRouterDescriptor: %v", err)
	}
	if before.GetSupportsSeasons() {
		t.Fatal("a build without the flag reads as supporting seasons")
	}

	version := "0.1.7"
	if err := store.Update(ctx, installation.ID, UpdateInstallationInput{Version: &version, Capabilities: manifestCapabilities(true)}); err != nil {
		t.Fatalf("update installation: %v", err)
	}
	after, err := service.RequestRouterDescriptor(ctx, installation.ID, "arr")
	if err != nil {
		t.Fatalf("RequestRouterDescriptor: %v", err)
	}
	if !after.GetSupportsSeasons() {
		t.Fatal("the upgraded build's supports_seasons was not read from stored metadata")
	}
}
