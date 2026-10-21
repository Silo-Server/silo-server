package apiv2

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
)

type fakeAdminSplit struct {
	calls, actor int
	req          handlers.AdminSplitRequest
	into         string
}

func (f *fakeAdminSplit) ListAdminItemFiles(context.Context, string, int, int) ([]handlers.AdminItemFileView, bool, error) {
	return []handlers.AdminItemFileView{{ID: 42, LibraryID: 7, FilePath: "/fixture/a.mkv", ObservedRootPath: "/fixture"}}, false, nil
}
func (f *fakeAdminSplit) SplitAdminItem(ctx context.Context, id string, req handlers.AdminSplitRequest) (handlers.AdminSplitResult, error) {
	f.calls++
	f.actor = middleware.GetUserID(ctx)
	f.req = req
	return handlers.AdminSplitResult{DryRun: req.DryRun, SourceContentID: id, TargetContentID: "target", FilesMoved: 1, Reattribution: &reattribute.Report{AmbiguousHistory: []reattribute.AmbiguousHistoryRow{{UserID: 7, ProfileID: "p", WatchedAt: "2026-01-02 03:04:05+00"}}}}, nil
}
func (f *fakeAdminSplit) MergeAdminItem(_ context.Context, _ string, into string) (string, error) {
	f.calls++
	f.into = into
	return into, nil
}
func TestAdminCatalogSplitTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminSplit{}
	deps.AdminCatalogSplit = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/source/"
	rec := do(t, h, "POST", path+"split", `{"file_ids":["42"],"target":{"content_id":"target"},"dry_run":true,"persist_override":null}`, bearer(adminToken))
	if rec.Code != 200 || !f.req.DryRun || f.req.PersistOverride != nil || f.req.FileIDs[0] != 42 || f.actor != 2 || !strings.Contains(rec.Body.String(), `"watched_at":"2026-01-02T03:04:05.000Z"`) {
		t.Fatalf("split %d %s %+v", rec.Code, rec.Body, f)
	}
	before := f.calls
	rec = do(t, h, "POST", path+"split", `{"file_ids":["0"],"target":{}}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "POST", path+"merge", `{"into":"target"}`, bearer(memberToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("member %d", rec.Code)
	}
	rec = do(t, h, "POST", path+"merge", `{"into":"target"}`, bearer(adminToken))
	if rec.Code != 200 || f.into != "target" {
		t.Fatalf("merge %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", path+"files", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"42"`) {
		t.Fatalf("files %d %s", rec.Code, rec.Body)
	}
}

func adminCatalogSplitFixtureCases() []fixtureCase {
	out := []fixtureCase{}
	for _, c := range []struct{ name, id, method, body, schema string }{{"files", "listAdminItemFiles", "GET", "", "CollectionAdminItemFile"}, {"split", "splitAdminItem", "POST", `{"file_ids":["42"],"target":{"content_id":"target"},"dry_run":true}`, "AdminSplitResult"}, {"merge", "mergeAdminItem", "POST", `{"into":"target"}`, "AdminMergeResult"}} {
		out = append(out, fixtureCase{name: "admin_item_" + c.name, operationID: c.id, method: c.method, path: Prefix + "/admin/items/source/" + c.name, body: c.body, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/" + c.schema, assertHeaders: []string{"Content-Type"}, scenario: "Administrator file grouping preserves synchronous repair and dry-run projection."})
	}
	return out
}
