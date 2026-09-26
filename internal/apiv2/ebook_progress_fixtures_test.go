package apiv2

func ebookProgressFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book-one/progress"
	return []fixtureCase{
		{name: "ebook_capability", operationID: "getEbookCapability", method: "GET", path: Prefix + "/capabilities/ebooks", headers: viewer, status: 200, schema: "#/components/schemas/EbookCapability", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Reader capability advertises ordered progress independently from Kindle conversion."},
		{name: "ebook_progress_absent", operationID: "getEbookProgress", method: "GET", path: path, headers: viewer, status: 200, schema: "#/components/schemas/EbookProgressOutputBody", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An accessible ebook without a saved position omits progress."},
		{name: "ebook_progress_saved", operationID: "saveEbookProgress", method: "PUT", path: path, headers: viewer, body: `{"file_id":"12","location":"epubcfi(/6/2)","progress":0.25,"updated_at":"2026-01-02T03:04:05.000Z"}`, status: 200, schema: "#/components/schemas/EbookProgressOutputBody", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "The progress write carries an opaque file ID and a stable client event time."},
		{name: "ebook_progress_missing_event_time", operationID: "saveEbookProgress", method: "PUT", path: path, headers: viewer, body: `{"file_id":"12","location":"here","progress":0.25}`, status: 422, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type"}, scenario: "Missing client event time is rejected before service dispatch."},
	}
}
