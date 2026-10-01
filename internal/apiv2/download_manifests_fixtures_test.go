package apiv2

func downloadManifestFixtureCases() []fixtureCase {
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	return []fixtureCase{
		{name: "download_manifest", operationID: "getDownloadManifest", method: "GET", path: Prefix + "/downloads/entry/manifest", headers: viewer, status: 200, schema: "#/components/schemas/DownloadManifest", assertHeaders: []string{"Content-Type"}, scenario: "A complete native offline manifest preserves identity, chapters and quality, with string media_file_id and authenticated v2 asset references."},
		{name: "download_manifest_page", operationID: "listDownloadBatchManifests", method: "GET", path: Prefix + "/downloads/batches/batch/manifests", headers: viewer, status: 200, schema: "#/components/schemas/DownloadManifestPage", assertHeaders: []string{"Content-Type"}, scenario: "A terminal batch page contains complete manifests and explicit skipped rows."},
	}
}
