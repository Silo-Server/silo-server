package apiv2

import "net/http"

func subtitleReadFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "subtitles_stored", operationID: "listStoredSubtitles", method: http.MethodGet, path: Prefix + "/subtitles/42", schema: "#/components/schemas/StoredSubtitles", scenario: "Stored tracks expose string IDs and public metadata without object keys or uploader identity.", status: 200, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}},
		{name: "subtitles_search_partial", operationID: "searchSubtitles", method: http.MethodPost, path: Prefix + "/subtitles/search", body: `{"media_file_id":"42","languages":["en"]}`, schema: "#/components/schemas/SubtitleSearchResults", scenario: "Partial provider results omit unknown dates and replace upstream error details with a safe warning.", status: 200, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
