package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeAdminProviderConfiguration struct {
	revision         int64
	reads, writes    int
	expected         *int64
	change           subtitles.ProviderConfigChange
	readErr, saveErr error
	result           handlers.AdminSubtitleProviderSaveResult
}

func (f *fakeAdminProviderConfiguration) GetAdminSubtitleProviderConfiguration(_ context.Context, name string) (handlers.AdminSubtitleProviderConfiguration, error) {
	f.reads++
	return handlers.AdminSubtitleProviderConfiguration{ProviderName: name, Revision: f.revision, Enabled: f.revision > 0, HasAPIKey: f.revision > 0}, f.readErr
}
func (f *fakeAdminProviderConfiguration) SaveAdminSubtitleProviderConfiguration(_ context.Context, _ string, change subtitles.ProviderConfigChange, expected *int64) (handlers.AdminSubtitleProviderSaveResult, error) {
	f.writes++
	f.expected = expected
	f.change = change
	return f.result, f.saveErr
}
func fixtureAdminProviderConfiguration() *fakeAdminProviderConfiguration {
	return &fakeAdminProviderConfiguration{revision: 4, result: handlers.AdminSubtitleProviderSaveResult{SavedRevision: 5, LocalApply: handlers.SubtitleProviderLocalApplied, LocalAppliedRevision: new(int64(9))}}
}
func adminProviderConfigurationFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_provider_configuration", operationID: "getAdminSubtitleProviderConfiguration", method: http.MethodGet, path: Prefix + "/admin/subtitle-providers/subdl", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminSubtitleProviderConfiguration", scenario: "Redacted canonical configuration supplies the captured edit validator."},
		{name: "admin_provider_configuration_saved", operationID: "updateAdminSubtitleProviderConfiguration", method: http.MethodPut, path: Prefix + "/admin/subtitle-providers/subdl", headers: with(actingRequestAdmin, "If-Match", "*"), body: `{"enabled":false}`, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminSubtitleProviderConfigurationSaved", scenario: "Confirmed durable revision5 is separate from local application of later revision9."},
	}
}
