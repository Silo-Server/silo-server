package apiv2

import "net/http"

func subtitleDownloadFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "subtitle_download", operationID: "downloadSubtitle", method: http.MethodPost, path: Prefix + "/subtitles/download", body: subtitleDownloadFixtureBody, schema: "#/components/schemas/SubtitleDownloadResult", scenario: "Single provider download returns public stored metadata and exact string IDs without object or uploader details.", status: 200, headers: viewerHeaders(), assertHeaders: []string{"Content-Type", "Cache-Control"}}}
}
