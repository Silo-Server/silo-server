package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminMatch struct {
	calls        int
	search       handlers.AdminMatchSearchRequest
	apply        handlers.AdminMatchApplyRequest
	rows         []metadata.MatchCandidate
	hints        handlers.AdminMatchHints
	hintsLibrary *int
	hintsErr     error
	failed       map[string]error
}

func (f *fakeAdminMatch) AdminItemMatchHints(_ context.Context, _ string, library *int) (handlers.AdminMatchHints, error) {
	f.calls++
	f.hintsLibrary = library
	return f.hints, f.hintsErr
}

func (f *fakeAdminMatch) SearchAdminItemMatches(_ context.Context, _ string, b handlers.AdminMatchSearchRequest) (handlers.AdminMatchSearchResult, error) {
	f.calls++
	f.search = b
	return handlers.AdminMatchSearchResult{Candidates: f.rows, ProviderErrors: f.failed}, nil
}
func (f *fakeAdminMatch) ApplyAdminItemMatch(_ context.Context, id string, b handlers.AdminMatchApplyRequest) (handlers.AdminMatchApplyResult, error) {
	f.calls++
	f.apply = b
	return handlers.AdminMatchApplyResult{ContentID: id, Updated: true}, nil
}

type matchItems struct{}

func (matchItems) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: id, Title: "Parasite", Type: "movie"}, nil
}

type matchFolders struct {
	ids []int
	err error
}

func (f matchFolders) GetFolderIDForItem(context.Context, string) (int, error) {
	return 0, errors.New("unused")
}

func (f matchFolders) GetFolderIDsForItem(context.Context, string) ([]int, error) {
	return f.ids, f.err
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

func TestAdminMatchSearchReportsFailedProvidersWithoutUpstreamDetail(t *testing.T) {
	deps := fixtureDeps()
	deps.AdminCatalogMatch = &fakeAdminMatch{failed: map[string]error{
		"tmdb": errors.New("GET https://upstream.invalid/search?api_key=secret: 503"),
	}}
	rec := do(t, newTestHandler(t, deps), "POST", Prefix+"/admin/items/item-1/match/search", `{}`, bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Candidates     []json.RawMessage `json:"candidates"`
		ProviderErrors map[string]string `json:"provider_errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Candidates) != 0 || len(body.ProviderErrors) != 1 || body.ProviderErrors["tmdb"] != "Metadata search failed" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
