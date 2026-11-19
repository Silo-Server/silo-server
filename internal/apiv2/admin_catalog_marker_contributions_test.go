package apiv2

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
)

type fakeAdminMarkerContributions struct {
	calls int
	body  handlers.MarkerContributionRequest
	pos   markers.ContributionPagePosition
	more  bool
}

func (f *fakeAdminMarkerContributions) ContributeAdminMarkers(_ context.Context, _ catalogsvc.AccessFilter, _ int, b handlers.MarkerContributionRequest) ([]handlers.MarkerContributionOutcomeView, error) {
	f.calls++
	f.body = b
	return []handlers.MarkerContributionOutcomeView{{Provider: "p", Segment: "intro", Status: "skipped", Reason: "already_submitted"}}, nil
}
func (f *fakeAdminMarkerContributions) ListAdminMarkerContributions(_ context.Context, _ catalogsvc.AccessFilter, id, limit int, p markers.ContributionPagePosition) ([]handlers.MarkerContributionView, bool, error) {
	f.calls++
	f.pos = p
	return []handlers.MarkerContributionView{{ID: "00000000-0000-0000-0000-000000000001", MediaFileID: id, Provider: "p", Segment: "intro", Status: "submitted", UpdatedAt: new(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))}}, f.more, nil
}
func adminCatalogMarkerContributionsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_marker_contribute", operationID: "contributeAdminFileMarkers", method: "POST", path: Prefix + "/admin/files/42/contribute", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminMarkerContributionOutcomes", assertHeaders: []string{"Content-Type"}, scenario: "Synchronous contribution returns existing per-provider outcomes."},
		{name: "admin_marker_contributions", operationID: "listAdminFileMarkerContributions", method: "GET", path: Prefix + "/admin/files/42/contributions", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminMarkerContribution", assertHeaders: []string{"Content-Type"}, scenario: "File contribution records use bounded keyset paging."},
	}
}
