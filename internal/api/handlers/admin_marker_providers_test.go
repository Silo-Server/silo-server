package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/markers"
)

func TestMarkerProviderResponseIncludesPluginMetadata(t *testing.T) {
	resp := toProviderConfigResponse(
		markers.ProviderConfig{Provider: "plugin:4:markers", FetchEnabled: true, FetchPriority: 25},
		true,
		markers.ProviderDescriptor{
			DisplayName:          "Plugin Markers",
			SourceType:           markers.ProviderSourcePlugin,
			PluginID:             "silo.plugin.markers",
			PluginInstallationID: 4,
			CapabilityID:         "markers",
		},
	)
	if resp.DisplayName != "Plugin Markers" || resp.SourceType != markers.ProviderSourcePlugin {
		t.Fatalf("plugin metadata fields missing: %+v", resp)
	}
	if resp.PluginID != "silo.plugin.markers" ||
		resp.PluginInstallationID != 4 ||
		resp.CapabilityID != "markers" ||
		!resp.IsSubmitter {
		t.Fatalf("plugin identity fields missing: %+v", resp)
	}
}
