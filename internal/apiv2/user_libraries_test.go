package apiv2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

type fakeUserLibraries struct {
	userID int
	scope  access.Scope
	calls  int
}

func (f *fakeUserLibraries) ListUserLibraries(ctx context.Context, userID int) ([]handlers.UserLibraryView, error) {
	f.calls++
	f.userID = userID
	f.scope, _ = access.GetScope(ctx)
	return []handlers.UserLibraryView{{ID: 12, Name: "Movies", Type: "movies", SortOrder: 2, PosterURL: "https://images.example/poster"}}, nil
}
func TestUserLibraryDiscoveryAuthority(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeUserLibraries)
	deps.UserLibraries = f
	h := NewHandler(deps)
	rec := do(t, h, "GET", Prefix+"/user/libraries", "", bearer(memberToken))
	var body Collection[UserLibrary]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || f.userID != 1 || len(body.Items) != 1 || body.Items[0].ID != "12" || body.Items[0].SortOrder != 2 {
		t.Fatal(rec.Code, rec.Body.String(), f.userID)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/user/libraries", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", Prefix+"/user/libraries", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	if f.calls != 1 {
		t.Fatal("denied request reached service")
	}
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{12}}})
	rec = do(t, NewHandler(deps), "GET", Prefix+"/user/libraries", "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if rec.Code != 200 || !f.scope.LibrariesRestricted || len(f.scope.AllowedLibraryIDs) != 1 || f.scope.AllowedLibraryIDs[0] != 12 {
		t.Fatal(rec.Code, rec.Body.String(), f.scope)
	}
	deps.UserLibraries = nil
	rec = do(t, NewHandler(deps), "GET", Prefix+"/user/libraries/capabilities", "", bearer(memberToken))
	var capability UserLibraryCapabilitiesOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &capability.Body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || capability.Body.Available {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, NewHandler(deps), "GET", Prefix+"/user/libraries", "", bearer(memberToken)), TypeDependencyUnavailable)
}
func userLibraryFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "user_libraries", operationID: "listUserLibraries", method: "GET", path: Prefix + "/user/libraries", headers: bearer(memberToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CollectionUserLibrary", scenario: "Account library discovery returns string IDs and the viewer projection without administrator paths or scan metadata."}}
}
