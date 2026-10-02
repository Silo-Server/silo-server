package apiv2

func downloadRegistryFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	return []fixtureCase{
		{name: "downloads_empty", operationID: "listDownloads", method: "GET", path: Prefix + "/downloads", headers: device, status: 200, schema: "#/components/schemas/CollectionDownloadEntry", assertHeaders: []string{"Content-Type"}, scenario: "An empty managed registry is a terminal bounded page."},
		{name: "download_status_event", operationID: "reportDownloadStatus", method: "PATCH", path: Prefix + "/downloads/entry", headers: device, body: `{"status":"completed","updated_at":"2026-01-02T03:04:05.000Z","revision":1}`, status: 200, schema: "#/components/schemas/DownloadEntry", assertHeaders: []string{"Content-Type"}, scenario: "A local completion report carries its retained event time and registry revision."},
		{name: "download_deleted", operationID: "deleteDownload", method: "DELETE", path: Prefix + "/downloads/entry", headers: device, status: 204, scenario: "Deleting a registry entry returns no representation."},
		{name: "download_capability", operationID: "getDownloadCapability", method: "GET", path: Prefix + "/capabilities/downloads", headers: viewer, status: 200, schema: "#/components/schemas/DownloadCapability", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Download policy exposes ordered status support independently of registry contents."},
	}
}
