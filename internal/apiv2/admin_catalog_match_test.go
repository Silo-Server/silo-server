package apiv2

import (
	"context"
	"errors"

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

type matchMetadata struct{}

func (matchMetadata) SearchAndNormalize(context.Context, metadata.SearchQuery, int) ([]metadata.MatchCandidate, error) {
	return nil, errors.New("unused")
}

func (matchMetadata) Process(context.Context, metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	return nil, errors.New("unused")
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
