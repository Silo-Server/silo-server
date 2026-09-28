package apiv2

func ebookAnnotationFixtureCases() []fixtureCase {
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/ebooks/book/annotations"
	return []fixtureCase{
		{name: "ebook_annotations_empty", operationID: "listEbookAnnotations", method: "GET", path: path, headers: viewer, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/CollectionEbookAnnotation", scenario: "A profile annotation page is bounded and carries an explicit terminal page."},
		{name: "ebook_annotation_created", operationID: "createEbookAnnotation", method: "POST", path: path, headers: viewer, body: `{"id":"intent","kind":"note","location":"chapter1","note":"saved"}`, status: 201, schema: "#/components/schemas/EbookAnnotation", assertHeaders: []string{"Content-Type", "ETag"}, scenario: "A client-selected ID creates one annotation and returns its mutation validator."},
		{name: "ebook_annotation_patch_missing_guard", operationID: "updateEbookAnnotation", method: "PATCH", path: path + "/intent", headers: viewer, body: `{"note":"changed"}`, status: 428, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/Problem", scenario: "An annotation edit requires the current validator."},
		{name: "ebook_annotation_delete_missing_guard", operationID: "deleteEbookAnnotation", method: "DELETE", path: path + "/intent", headers: viewer, status: 428, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/Problem", scenario: "An annotation deletion requires the current validator."},
	}
}
