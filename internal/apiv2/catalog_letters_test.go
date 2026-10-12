package apiv2

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

func TestQueryCatalogLetters(t *testing.T) {
	deps, fake := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/catalog/letters",
		`{"library_id":"1","type":"movie","order":"desc","match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"contains","value":"Crime"}]}]}`,
		viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var body CatalogLetters
	decodeJSON(t, rec.Body, &body)
	if body.Total != 5 || len(body.Letters) != 2 || body.Letters[1] != (CatalogLetter{Letter: "H", Count: 3, Position: 1}) ||
		len(body.Runs) != 3 || body.Runs[2] != (CatalogLetterRun{Letter: "#", Position: 4}) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	req := fake.lastReq
	if req.Source != catalogpkg.CatalogSourceQuery || req.Query.Sort.Field != "title" || req.Query.Sort.Order != "desc" ||
		req.Query.MediaScope != "movie" || len(req.Query.LibraryIDs) != 1 || req.Query.LibraryIDs[0] != 1 ||
		len(req.Query.Groups) != 1 || req.Query.Groups[0].Rules[0].Field != "genre" {
		t.Fatalf("seam request = %+v", req)
	}
	if fake.lastViewer.ProfileID != "p-owner" {
		t.Fatalf("viewer = %+v", fake.lastViewer)
	}

	// Title order ascending is the default.
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/letters", `{}`, viewerHeaders())
	if rec.Code != http.StatusOK || fake.lastReq.Query.Sort.Order != "asc" {
		t.Fatalf("default order: %d %s order=%q", rec.Code, rec.Body.String(), fake.lastReq.Query.Sort.Order)
	}

	// Unknown rule fields and resolver refusals are validation problems on the body.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/letters",
		`{"groups":[{"match":"all","rules":[{"field":"nope","op":"is","value":"x"}]}]}`, viewerHeaders()), TypeValidationFailed)
	fake.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "invalid catalog request"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/letters", `{}`, viewerHeaders()), TypeValidationFailed)
}

func catalogLettersFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "query_catalog_letters_ok", operationID: opQueryCatalogLetters,
			scenario: "The letter index of a library sorted by title: each letter with titles, its count, and its first list position.",
			method:   http.MethodPost, path: "/api/v2/catalog/letters", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"library_id":"1","type":"movie","order":"asc"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogLetters"},
	}
}
