package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"strings"
	"testing"
	"time"
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
func TestAdminMarkerHistoryTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminMarkerHistory{}
	deps.AdminMarkerHistory = f
	deps.CatalogAccess = &fakeCatalog{}
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		path   string
		target handlers.MarkerTarget
	}{{"/history", handlers.MarkerTarget{}}, {"/files/42/history", handlers.MarkerTarget{FileID: 42}}, {"/items/episode-1/history", handlers.MarkerTarget{ItemID: "episode-1"}}} {
		path := Prefix + "/admin/markers" + tc.path
		rec := do(t, h, "GET", path, "", bearer(adminToken))
		if rec.Code != 200 || f.limit != 25 || f.target != tc.target {
			t.Fatalf("%s: %d %s target=%+v limit=%d", path, rec.Code, rec.Body, f.target, f.limit)
		}
		for _, part := range []string{`"id":"9007199254740993"`, `"api_key_id":"9007199254740995"`, `"start_seconds":1.5`, `"created_at":"2026-01-02T02:04:05.000Z"`} {
			if !strings.Contains(rec.Body.String(), part) {
				t.Fatalf("missing %s: %s", part, rec.Body)
			}
		}
		if strings.Contains(rec.Body.String(), `"after"`) {
			t.Fatalf("absent snapshot: %s", rec.Body)
		}
		before := f.calls
		for _, limit := range []string{"0", "101", "invalid"} {
			rec = do(t, h, "GET", path+"?limit="+limit, "", bearer(adminToken))
			if rec.Code != 422 || f.calls != before {
				t.Fatalf("invalid limit: %d %s", rec.Code, rec.Body)
			}
		}
		rec = do(t, h, "GET", path, "", bearer(memberToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("member: %d", rec.Code)
		}
	}
	rec := do(t, h, "GET", Prefix+"/admin/markers/files/0/history", "", bearer(adminToken))
	if rec.Code != 422 {
		t.Fatalf("file id: %d", rec.Code)
	}
}
func adminCatalogMarkerHistoryFixtureCases() []fixtureCase {
	cases := []fixtureCase{}
	for _, tc := range []struct{ name, path, id string }{{"all", "/history", "listAdminMarkerHistory"}, {"file", "/files/42/history", "listAdminFileMarkerHistory"}, {"item", "/items/episode-1/history", "listAdminItemMarkerHistory"}} {
		cases = append(cases, fixtureCase{name: "admin_marker_history_" + tc.name, operationID: tc.id, method: "GET", path: Prefix + "/admin/markers" + tc.path, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminMarkerHistory", assertHeaders: []string{"Content-Type"}, scenario: "Bounded recent marker edits preserve opaque identifiers and canonical snapshots."})
	}
	return cases
}
