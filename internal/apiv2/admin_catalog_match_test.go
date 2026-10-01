package apiv2

import (
	"context"
	"encoding/json"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/policy"
	"testing"
)

type fakeAdminMatch struct {
	calls  int
	search handlers.AdminMatchSearchRequest
	apply  handlers.AdminMatchApplyRequest
	rows   []metadata.MatchCandidate
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
	f := &fakeAdminMatch{rows: []metadata.MatchCandidate{{Title: "first"}, {Title: "second"}}}
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
	before := f.calls
	for _, suffix := range []string{"search", "apply"} {
		rec = do(t, h, "POST", Prefix+"/admin/items/forbidden/match/"+suffix, `{"provider_ids":{"tmdb":"42"}}`, bearer(adminToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("gate %d", rec.Code)
		}
	}
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
