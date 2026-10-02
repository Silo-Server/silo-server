package apiv2

import "net/http"

func adminSubtitleInspectionFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_subtitle_provider_configuration", operationID: "listAdminSubtitleProviders", method: http.MethodGet, path: Prefix + "/admin/subtitle-providers", schema: "#/components/schemas/AdminSubtitleProvidersOutputBody", scenario: "Saved configuration exposes only presence flags; an unsaved provider omits update time.", status: 200, headers: actingRequestAdmin, assertHeaders: []string{"Content-Type", "Cache-Control"}},
		{name: "admin_subtitle_provider_test_redacted", operationID: "testAdminSubtitleProvider", method: http.MethodPost, path: Prefix + "/admin/subtitle-providers/subdl/test", body: `{}`, schema: "#/components/schemas/SubtitleProviderTestView", scenario: "Upstream failure details do not expose credentials or URLs.", status: 200, headers: actingRequestAdmin, assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
