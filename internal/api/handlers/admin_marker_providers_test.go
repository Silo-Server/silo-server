package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/markers"
)

type fakeMarkerStatsSubmitter struct{}

func (fakeMarkerStatsSubmitter) ID() string { return "introdb" }
func (fakeMarkerStatsSubmitter) FetchMarkers(context.Context, markers.Request) (markers.Result, error) {
	return markers.Result{}, nil
}
func (fakeMarkerStatsSubmitter) SubmitMarker(context.Context, markers.SubmissionRequest) (markers.SubmissionResult, error) {
	return markers.SubmissionResult{}, nil
}
func (fakeMarkerStatsSubmitter) FetchUserStats(context.Context) (markers.UserStats, error) {
	return markers.UserStats{Total: 10, Accepted: 7, Pending: 2, Rejected: 1, AcceptanceRate: 0.7, CurrentStreak: 3, BestStreak: 5}, nil
}

type fakePluginMarkerSubmitter struct{ fakeMarkerStatsSubmitter }

func (fakePluginMarkerSubmitter) ID() string { return "plugin:6:introdb" }

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
