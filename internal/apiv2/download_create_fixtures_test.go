package apiv2

func downloadCreateFixtureCases() []fixtureCase {
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	return []fixtureCase{
		{name: "download_create_single", operationID: "createDownloads", method: "POST", path: Prefix + "/downloads", headers: viewer, body: `{"content_id":"movie","media_file_id":"42","expected_revision":0}`, status: 202, schema: "#/components/schemas/DownloadCreated", assertHeaders: []string{"Content-Type"}, scenario: "Managed creation carries explicit absence authority and returns complete registry identity in the collection envelope."},
		{name: "download_create_page", operationID: "createDownloads", method: "POST", path: Prefix + "/downloads", headers: viewer, body: `{"content_id":"series","series":true,"season_number":0,"batch_id":"intent"}`, status: 202, schema: "#/components/schemas/DownloadCreated", assertHeaders: []string{"Content-Type"}, scenario: "A bounded specials page can contain no new files and report the skipped episode without truncating a later page."},
	}
}
