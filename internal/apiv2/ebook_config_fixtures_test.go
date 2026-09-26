package apiv2

func ebookConfigFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book/reader-config"
	return []fixtureCase{
		{name: "ebook_config_default", operationID: "getEbookReaderConfig", method: "GET", path: path, headers: viewer, status: 200, schema: "#/components/schemas/EbookConfig", assertHeaders: []string{"Content-Type", "ETag"}, scenario: "A never-saved configuration has a scoped validator for its empty default."},
		{name: "ebook_config_missing_guard", operationID: "saveEbookReaderConfig", method: "PUT", path: path, headers: viewer, body: `{"config":{"settings":{"theme":"dark"}}}`, status: 428, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type"}, scenario: "Configuration replacement requires the current validator."},
		{name: "ebook_config_saved", operationID: "saveEbookReaderConfig", method: "PUT", path: path, headers: with(viewer, "If-Match", ebookConfigTag(1, "p-owner", "book", nil).String()), body: `{"config":{"settings":{"theme":"dark"}}}`, status: 200, schema: "#/components/schemas/EbookConfig", assertHeaders: []string{"Content-Type", "ETag"}, scenario: "A guarded first write stores a bounded client-owned JSON object."},
	}
}
