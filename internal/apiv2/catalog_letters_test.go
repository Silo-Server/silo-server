package apiv2

import (
	"net/http"
)

func catalogLettersFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "query_catalog_letters_ok", operationID: opQueryCatalogLetters,
			scenario: "The letter index of a library sorted by title: each letter with titles, its count, and its first list position.",
			method:   http.MethodPost, path: "/api/v2/catalog/letters", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"library_id":"1","type":"movie","order":"asc"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogLetters"},
	}
}
