package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

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

func TestValidateMarkerProviderUsesSnakeCaseStats(t *testing.T) {
	reg := markers.NewRegistry(nil)
	if err := reg.Register(fakeMarkerStatsSubmitter{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	h := NewAdminMarkerProvidersHandler(reg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/admin/markers/providers/introdb/validate", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "introdb")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleValidateProvider(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"acceptance_rate":0.7`) || strings.Contains(body, "AcceptanceRate") {
		t.Fatalf("unexpected stats response shape: %s", body)
	}
}

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
