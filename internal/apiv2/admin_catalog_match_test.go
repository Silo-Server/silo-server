package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/policy"
)

type fakeAdminMatch struct {
	calls        int
	search       handlers.AdminMatchSearchRequest
	apply        handlers.AdminMatchApplyRequest
	rows         []metadata.MatchCandidate
	hints        handlers.AdminMatchHints
	hintsLibrary *int
	hintsErr     error
}

func (f *fakeAdminMatch) AdminItemMatchHints(_ context.Context, _ string, library *int) (handlers.AdminMatchHints, error) {
	f.calls++
	f.hintsLibrary = library
	return f.hints, f.hintsErr
}

func (f *fakeAdminMatch) SearchAdminItemMatches(_ context.Context, _ string, b handlers.AdminMatchSearchRequest) (handlers.AdminMatchSearchResult, error) {
	f.calls++
	f.search = b
	return handlers.AdminMatchSearchResult{Candidates: f.rows}, nil
}
func (f *fakeAdminMatch) ApplyAdminItemMatch(_ context.Context, id string, b handlers.AdminMatchApplyRequest) (handlers.AdminMatchApplyResult, error) {
	f.calls++
	f.apply = b
	return handlers.AdminMatchApplyResult{ContentID: id, Updated: true}, nil
}
func TestAdminMatchPermissionAndProjection(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminMatch{
		rows:  []metadata.MatchCandidate{{Title: "first"}, {Title: "second"}},
		hints: handlers.AdminMatchHints{Title: "Parasite", Year: 2019, ProviderIDs: map[string]string{"tmdb": "496243"}},
	}
	deps.AdminCatalogMatch = f
	deps.PermissionGates[policy.PermissionMetadataCuration] = adminTranslationGate
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/item-1/match/"
	rec := do(t, h, "POST", path+"search", `{"title":"query","library_id":"7","limit":1}`, bearer(memberToken))
	var body AdminMatchCandidates
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Candidates) != 1 || body.Candidates[0].Title != "first" || !body.Truncated || f.search.LibraryID == nil || *f.search.LibraryID != 7 || body.Candidates[0].Sources == nil {
		t.Fatalf("search %d %s", rec.Code, rec.Body)
	}
	if f.rows[0].Sources != nil {
		t.Fatal("projection mutated service rows")
	}
	rec = do(t, h, "POST", path+"apply", `{"provider_ids":{"tmdb":"42"},"library_id":"7"}`, bearer(memberToken))
	if rec.Code != 200 || f.apply.ProviderIDs["tmdb"] != "42" {
		t.Fatalf("apply %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", path+"hints?library_id=7", "", bearer(memberToken))
	var hints AdminMatchHints
	if err := json.Unmarshal(rec.Body.Bytes(), &hints); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || hints.Title != "Parasite" || hints.Year != 2019 || hints.ProviderIDs["tmdb"] != "496243" || f.hintsLibrary == nil || *f.hintsLibrary != 7 {
		t.Fatalf("hints %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", Prefix+"/admin/catalog/capabilities", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"item_match_hints":true`) {
		t.Fatalf("capabilities %d %s", rec.Code, rec.Body)
	}
	before := f.calls
	for _, suffix := range []string{"search", "apply"} {
		rec = do(t, h, "POST", Prefix+"/admin/items/forbidden/match/"+suffix, `{"provider_ids":{"tmdb":"42"}}`, bearer(adminToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("gate %d", rec.Code)
		}
	}
	rec = do(t, h, "GET", Prefix+"/admin/items/forbidden/match/hints", "", bearer(adminToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("hints gate %d", rec.Code)
	}
	f.hints = handlers.AdminMatchHints{}
	rec = do(t, h, "GET", path+"hints", "", bearer(memberToken))
	if rec.Code != 200 || f.hintsLibrary != nil || !strings.Contains(rec.Body.String(), `"provider_ids":{}`) {
		t.Fatalf("empty hints %d %s", rec.Code, rec.Body)
	}
	f.hintsErr = &handlers.APIError{Status: 400, Code: "bad_request", Message: "Invalid library_id"}
	rec = do(t, h, "GET", path+"hints?library_id=9", "", bearer(memberToken))
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"location":"query.library_id"`) {
		t.Fatalf("foreign library hints %d %s", rec.Code, rec.Body)
	}
	before = f.calls
	for _, body := range []string{`{"limit":0}`, `{"limit":501}`, `{"library_id":"0"}`} {
		rec = do(t, h, "POST", path+"search", body, bearer(memberToken))
		if rec.Code != 422 || f.calls != before {
			t.Fatalf("invalid %d %s", rec.Code, rec.Body)
		}
	}
	f.rows = nil
	rec = do(t, h, "POST", path+"search", `{}`, bearer(memberToken))
	if rec.Code != 200 {
		t.Fatalf("default %d %s", rec.Code, rec.Body)
	}
	deps.PermissionGates = nil
	h = newTestHandler(t, deps)
	rec = do(t, h, "POST", path+"apply", `{"provider_ids":{}}`, bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("missing gate %d", rec.Code)
	}
}
func adminCatalogMatchFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_match_search", operationID: "searchAdminItemMatches", method: "POST", path: Prefix + "/admin/items/item-1/match/search", body: `{}`, headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminMatchCandidates", assertHeaders: []string{"Content-Type"}, scenario: "Delegated curator searches bounded provider candidates."},
		{name: "admin_match_apply", operationID: "applyAdminItemMatch", method: "POST", path: Prefix + "/admin/items/item-1/match/apply", body: `{"provider_ids":{"tmdb":"42"}}`, headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminMatchApplied", assertHeaders: []string{"Content-Type"}, scenario: "Match selection uses the existing synchronous metadata service."},
	}
}

// adminCatalogMatchHintsFixtureCases runs last so its request does not shift
// the request numbers recorded in earlier problem fixtures.
func adminCatalogMatchHintsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_match_hints", operationID: "getAdminItemMatchHints", method: "GET", path: Prefix + "/admin/items/item-1/match/hints", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminMatchHints", assertHeaders: []string{"Content-Type"}, scenario: "Delegated curator reads the title, year, and provider IDs the scanner parsed from the item's folder."},
	}
}
