package apiv2

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeAdminMarkerHistory struct {
	calls, limit int
	target       handlers.MarkerTarget
}

func (f *fakeAdminMarkerHistory) AdminMarkerHistory(_ context.Context, _ catalogsvc.AccessFilter, target handlers.MarkerTarget, limit int) ([]handlers.MarkerEditAuditView, error) {
	f.calls++
	f.target = target
	f.limit = limit
	return []handlers.MarkerEditAuditView{{ID: 9007199254740993, MediaFileID: 42, Segment: "intro", Action: "clear", UserID: new(7), APIKeyID: new(int64(9007199254740995)), Before: &handlers.MarkerSegmentView{Start: new(1.5), End: new(10.0)}, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))}}, nil
}
func adminCatalogMarkerHistoryFixtureCases() []fixtureCase {
	cases := []fixtureCase{}
	for _, tc := range []struct{ name, path, id string }{{"all", "/history", "listAdminMarkerHistory"}, {"file", "/files/42/history", "listAdminFileMarkerHistory"}, {"item", "/items/episode-1/history", "listAdminItemMarkerHistory"}} {
		cases = append(cases, fixtureCase{name: "admin_marker_history_" + tc.name, operationID: tc.id, method: "GET", path: Prefix + "/admin/markers" + tc.path, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminMarkerHistory", assertHeaders: []string{"Content-Type"}, scenario: "Bounded recent marker edits preserve opaque identifiers and canonical snapshots."})
	}
	return cases
}
