package apiv2

import "net/http"

func subtitleAICreateFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "subtitle_ai_create", operationID: "createSubtitleAIJob", method: http.MethodPost, path: Prefix + "/subtitles/ai/translate", scenario: "A single authorized request returns exact string job/file IDs and reports whether best-effort live delivery was attached.", schema: "#/components/schemas/SubtitleAICreateOutputBody", status: 202, headers: viewerHeaders(), body: `{"media_file_id":"42","kind":"translate","source_index":3,"source_language":"en","target_language":"fr","session_id":"session","start_position":12.5}`, assertHeaders: []string{"Content-Type", "Cache-Control"}}}
}
