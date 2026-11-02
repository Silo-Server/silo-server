package plugins

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

// The host reads supports_seasons, reports_download_progress and
// api_key_optional from the capability metadata stored from the manifest, the
// way install records it.
func TestRequestRouterDescriptorReadsStoredManifestFlag(t *testing.T) {
	records, err := CapabilityRecordsFromManifest(&pluginv1.PluginManifest{
		Capabilities: []*pluginv1.CapabilityDescriptor{
			{Type: capability.RequestRouter, Id: "arr", RequestRouter: &pluginv1.RequestRouterDescriptor{SupportsSeasons: true}},
			{Type: capability.RequestRouter, Id: "legacy"},
			{Type: capability.RequestRouter, Id: "progress", RequestRouter: &pluginv1.RequestRouterDescriptor{SupportsSeasons: true, ReportsDownloadProgress: true}},
			{Type: capability.RequestRouter, Id: "keyless", RequestRouter: &pluginv1.RequestRouterDescriptor{ApiKeyOptional: true}},
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
		capabilityID    string
		want            bool
		wantProgress    bool
		wantKeyOptional bool
	}{
		{"arr", true, false, false},      // built before reports_download_progress existed
		{"legacy", false, false, false},  // built before the descriptor existed
		{"missing", false, false, false}, // not declared at all
		{"progress", true, true, false},
		{"keyless", false, false, true},
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
		if got.GetApiKeyOptional() != tc.wantKeyOptional {
			t.Errorf("%s: api_key_optional = %v, want %v", tc.capabilityID, got.GetApiKeyOptional(), tc.wantKeyOptional)
		}
	}
}
