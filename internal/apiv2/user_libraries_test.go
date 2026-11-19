package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeUserLibraries struct {
	userID        int
	scope         access.Scope
	calls         int
	includeHidden bool
}

func (f *fakeUserLibraries) ListUserLibraries(ctx context.Context, userID int, includeHidden bool) ([]handlers.UserLibraryView, error) {
	f.calls++
	f.userID = userID
	f.includeHidden = includeHidden
	f.scope, _ = access.GetScope(ctx)
	return []handlers.UserLibraryView{{ID: 12, Name: "Movies", Type: "movies", SortOrder: 2, PosterURL: "https://images.example/poster"}}, nil
}
func userLibraryFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "user_libraries", operationID: "listUserLibraries", method: "GET", path: Prefix + "/user/libraries", headers: bearer(memberToken), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CollectionUserLibrary", scenario: "Account library discovery returns string IDs and the viewer projection without administrator paths or scan metadata."}}
}
