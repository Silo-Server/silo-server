package apiv2

import (
	"context"
	"net/http"
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

type fakeAdminItemFiles struct {
	fakeAdminSplit
	requests []struct {
		itemID       string
		limit, after int
	}
}

func (f *fakeAdminItemFiles) ListAdminItemFiles(_ context.Context, itemID string, limit, after int) ([]handlers.AdminItemFileView, bool, error) {
	f.requests = append(f.requests, struct {
		itemID       string
		limit, after int
	}{itemID, limit, after})
	for _, id := range []int{42, 57, 90} {
		if id > after {
			return []handlers.AdminItemFileView{{ID: id, LibraryID: 7}}, id != 90, nil
		}
	}
	return nil, false, nil
}

func TestAdminItemFilesCursorContinuation(t *testing.T) {
	f := &fakeAdminItemFiles{}
	deps := pilotDeps(nil, nil)
	deps.AdminCatalogSplit = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/source/files?limit=1"
	cursor := ""
	for i, want := range []ID{"42", "57", "90"} {
		pagePath := path
		if cursor != "" {
			pagePath += "&cursor=" + cursor
		}
		rec := do(t, h, http.MethodGet, pagePath, "", bearer(adminToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", i, rec.Code, rec.Body.String())
		}
		var page Collection[AdminItemFile]
		decodeBody(t, rec.Body, &page)
		if len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("page %d items = %+v, want file %q", i, page.Items, want)
		}
		if len(f.requests) != i+1 {
			t.Fatalf("page %d service requests = %d", i, len(f.requests))
		}
		request := f.requests[i]
		if request.itemID != "source" || request.limit != 1 || request.after != []int{0, 42, 57}[i] {
			t.Fatalf("page %d service request = %+v", i, request)
		}
		cursor = page.Page.NextCursor
		if (i < 2) != (cursor != "") {
			t.Fatalf("page %d next cursor = %q", i, cursor)
		}
		if i == 0 {
			for _, invalidPath := range []string{
				Prefix + "/admin/items/other/files?limit=1&cursor=" + cursor,
				Prefix + "/admin/items/source/files?limit=2&cursor=" + cursor,
				path + "&cursor=not-a-cursor",
			} {
				requireProblem(t, do(t, h, http.MethodGet, invalidPath, "", bearer(adminToken)), TypeInvalidCursor)
			}
			if len(f.requests) != 1 {
				t.Fatal("invalid cursor reached the service")
			}
		}
	}
}

func adminCatalogSplitFixtureCases() []fixtureCase {
	out := []fixtureCase{}
	for _, c := range []struct{ name, id, method, body, schema string }{{"files", "listAdminItemFiles", "GET", "", "CollectionAdminItemFile"}, {"split", "splitAdminItem", "POST", `{"file_ids":["42"],"target":{"content_id":"target"},"dry_run":true}`, "AdminSplitResult"}, {"merge", "mergeAdminItem", "POST", `{"into":"target"}`, "AdminMergeResult"}} {
		out = append(out, fixtureCase{name: "admin_item_" + c.name, operationID: c.id, method: c.method, path: Prefix + "/admin/items/source/" + c.name, body: c.body, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/" + c.schema, assertHeaders: []string{"Content-Type"}, scenario: "Administrator file grouping preserves synchronous repair and dry-run projection."})
	}
	return out
}
