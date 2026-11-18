package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminUnmatched struct{ calls int }

func (f *fakeAdminUnmatched) ListAdminUnmatchedFiles(_ context.Context, limit, after int) ([]handlers.AdminUnmatchedFileView, bool, error) {
	f.calls++
	if after > 0 {
		return []handlers.AdminUnmatchedFileView{}, false, nil
	}
	return []handlers.AdminUnmatchedFileView{{ID: 42, MediaFolderID: 7, FilePath: "fixture.mkv", FileSize: 100, Container: "mkv"}}, true, nil
}
func adminCatalogUnmatchedFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_unmatched_files", operationID: "listAdminUnmatchedFiles", method: "GET", path: Prefix + "/admin/unmatched?limit=1", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminUnmatchedFile", assertHeaders: []string{"Content-Type"}, scenario: "Unmatched files retain opaque IDs and bounded live continuation."}}
}
