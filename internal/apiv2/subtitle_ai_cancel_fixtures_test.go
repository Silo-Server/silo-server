package apiv2

import "net/http"

func subtitleAICancelFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "subtitle_ai_cancel", operationID: "cancelSubtitleAIJob", method: http.MethodPost, path: Prefix + "/subtitles/ai/jobs/9007199254740993/cancel", scenario: "Cancellation preserves an opaque job identifier and acknowledges the state request with an empty response; read job state to resolve completion races.", status: 204, headers: viewerHeaders(), assertHeaders: []string{"Cache-Control"}}}
}
