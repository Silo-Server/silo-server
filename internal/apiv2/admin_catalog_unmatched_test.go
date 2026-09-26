package apiv2

import (
	"context"
	"encoding/json"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"testing"
)

type fakeAdminUnmatched struct{ calls int }

func (f *fakeAdminUnmatched) ListAdminUnmatchedFiles(_ context.Context, limit, after int) ([]handlers.AdminUnmatchedFileView, bool, error) {
	f.calls++
	if after > 0 {
		return []handlers.AdminUnmatchedFileView{}, false, nil
	}
	return []handlers.AdminUnmatchedFileView{{ID: 42, MediaFolderID: 7, FilePath: "fixture.mkv", FileSize: 100, Container: "mkv"}}, true, nil
}
func TestAdminUnmatchedCursorAndAuthority(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminUnmatched{}
	deps.AdminUnmatchedFiles = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/unmatched"
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var out Collection[AdminUnmatchedFile]
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(out.Items) != 1 || out.Items[0].ID != "42" || out.Items[0].MediaFolderID != "7" {
		t.Fatalf("first %d %s", rec.Code, rec.Body)
	}
	var raw struct {
		Page struct {
			NextCursor string `json:"next_cursor"`
		} `json:"page"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if raw.Page.NextCursor == "" {
		t.Fatal("missing cursor")
	}
	rec = do(t, h, "GET", path+"?limit=1&cursor="+raw.Page.NextCursor, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("next %d %s", rec.Code, rec.Body)
	}
	before := f.calls
	rec = do(t, h, "GET", path+"?limit=2&cursor="+raw.Page.NextCursor, "", bearer(adminToken))
	if rec.Code != 400 || f.calls != before {
		t.Fatalf("scope %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", path, "", bearer(memberToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("auth %d", rec.Code)
	}
}
func adminCatalogUnmatchedFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_unmatched_files", operationID: "listAdminUnmatchedFiles", method: "GET", path: Prefix + "/admin/unmatched?limit=1", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminUnmatchedFile", assertHeaders: []string{"Content-Type"}, scenario: "Unmatched files retain opaque IDs and bounded live continuation."}}
}
