package apiv2

import (
	"context"
	"encoding/json"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
	"strings"
	"testing"
	"time"
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
func TestAdminMarkerContributionsTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminMarkerContributions{}
	deps.AdminMarkerContributions = f
	deps.CatalogAccess = &fakeCatalog{}
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/files/42/"
	for _, body := range []string{"", `{"provider":"p","segments":["intro"]}`} {
		rec := do(t, h, "POST", path+"contribute", body, bearer(adminToken))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"skipped"`) {
			t.Fatalf("submit: %d %s", rec.Code, rec.Body)
		}
	}
	before := f.calls
	rec := do(t, h, "POST", path+"contribute", `{"segments":["unknown"]}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "POST", path+"contribute", "", bearer(memberToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("member %d", rec.Code)
	}
	f.more = true
	rec = do(t, h, "GET", path+"contributions?limit=1", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var page Collection[AdminMarkerContribution]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page == nil || !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("page %+v", page)
	}
	cursor := page.Page.NextCursor
	f.more = false
	rec = do(t, h, "GET", path+"contributions?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 200 || f.pos.ID == "" {
		t.Fatalf("continue %d %s", rec.Code, rec.Body)
	}
	before = f.calls
	rec = do(t, h, "GET", Prefix+"/admin/files/43/contributions?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 400 || f.calls != before {
		t.Fatalf("scope %d %s", rec.Code, rec.Body)
	}
}
func adminCatalogMarkerContributionsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_marker_contribute", operationID: "contributeAdminFileMarkers", method: "POST", path: Prefix + "/admin/files/42/contribute", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminMarkerContributionOutcomes", assertHeaders: []string{"Content-Type"}, scenario: "Synchronous contribution returns existing per-provider outcomes."},
		{name: "admin_marker_contributions", operationID: "listAdminFileMarkerContributions", method: "GET", path: Prefix + "/admin/files/42/contributions", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminMarkerContribution", assertHeaders: []string{"Content-Type"}, scenario: "File contribution records use bounded keyset paging."},
	}
}
