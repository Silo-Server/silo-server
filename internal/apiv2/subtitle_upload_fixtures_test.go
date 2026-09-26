package apiv2

import "net/http"

func subtitleUploadFixtureCases() []fixtureCase {
	file := fixtureMultipart("file", "synthetic.en.srt", "application/octet-stream", "synthetic subtitle")
	fields := "--silo-fixture-boundary\r\nContent-Disposition: form-data; name=\"media_file_id\"\r\n\r\n42\r\n--silo-fixture-boundary\r\nContent-Disposition: form-data; name=\"language\"\r\n\r\nen\r\n"
	return []fixtureCase{
		{name: "subtitle_upload", operationID: "uploadSubtitle", method: http.MethodPost, path: Prefix + "/subtitles/upload", body: fields + file, headers: with(viewerHeaders(), "Content-Type", fixtureMultipartType), schema: "#/components/schemas/SubtitleDownloadResult", scenario: "A bounded subtitle multipart upload returns public stored metadata with string IDs.", status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}},
		{name: "subtitle_detect_language", operationID: "detectSubtitleLanguage", method: http.MethodPost, path: Prefix + "/subtitles/detect-language", body: file, headers: with(viewerHeaders(), "Content-Type", fixtureMultipartType), schema: "#/components/schemas/SubtitleDetection", scenario: "Filename language detection does not persist uploaded content.", status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}
}
