package apiv2

import "net/http"

func subtitleAIReadFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "subtitle_ai_job_opaque_id", operationID: "getSubtitleAIJob", method: http.MethodGet, path: Prefix + "/subtitles/ai/jobs/9007199254740993", schema: "#/components/schemas/SubtitleAIJobOutputBody", scenario: "Job IDs above JavaScript safe integer range remain strings; upstream errors are redacted and missing result stays null.", status: 200, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}},
		{name: "subtitle_ai_quota", operationID: "getSubtitleAIQuota", method: http.MethodGet, path: Prefix + "/subtitles/ai/quota", schema: "#/components/schemas/QuotaStatus", scenario: "The selected viewer receives account-scoped transcription budget counters.", status: 200, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
