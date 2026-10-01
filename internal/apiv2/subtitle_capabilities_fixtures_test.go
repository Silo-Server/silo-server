package apiv2

import "net/http"

func subtitleCapabilityFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "subtitle_providers_disabled", operationID: "getSubtitleProviderStatus", method: http.MethodGet, path: Prefix + "/subtitles/providers/status", schema: "#/components/schemas/SubtitleProviderStatus", scenario: "Missing providers remain discoverable as disabled with an empty list.", status: 200, headers: bearer(memberToken), assertHeaders: []string{"Content-Type", "Cache-Control"}},
		{name: "subtitle_ai_disabled", operationID: "getSubtitleAIStatus", method: http.MethodGet, path: Prefix + "/subtitles/ai/status", schema: "#/components/schemas/SubtitleAIStatus", scenario: "Missing AI configuration reports both engine capabilities disabled.", status: 200, headers: bearer(memberToken), assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
