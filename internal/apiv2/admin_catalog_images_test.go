package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminImages struct {
	calls   int
	rows    []handlers.AdminImageEntryView
	request handlers.AdminItemImageRequest
}

func (f *fakeAdminImages) GetAdminItemImages(context.Context, string) (handlers.AdminItemImagesView, error) {
	f.calls++
	return handlers.AdminItemImagesView{Images: f.rows, ProviderErrors: map[string]string{"provider": "private upstream details"}}, nil
}
func (f *fakeAdminImages) ApplyAdminItemImage(_ context.Context, id string, r handlers.AdminItemImageRequest) (handlers.AdminItemImageResult, error) {
	f.calls++
	f.request = r
	return handlers.AdminItemImageResult{ContentID: id, StoredPath: "immutable-artwork", Revision: "revision", Thumbhash: "hash"}, nil
}
func TestAdminImagePagesAndApply(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminImages{rows: []handlers.AdminImageEntryView{{ProviderID: "p", OriginalURL: "b", URL: "display-b", Type: "poster", Rating: 9}, {ProviderID: "p", OriginalURL: "a", URL: "display-a", Type: "poster", Rating: 1}}}
	deps.AdminCatalogImages = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/item-1/images"
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var page AdminImagesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(page.Items) != 1 || !page.Page.HasMore || page.Items[0].OriginalURL != "b" || strings.Contains(rec.Body.String(), "private upstream") {
		t.Fatalf("first %d %s", rec.Code, rec.Body)
	}
	if f.rows[0].OriginalURL != "b" {
		t.Fatal("sorted service slice")
	}
	cursor := page.Page.NextCursor
	f.rows[0].URL = "rotated display URL"
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("rotated display %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].OriginalURL != "a" || page.Page.HasMore {
		t.Fatalf("continuation lost rating order: %+v", page)
	}
	f.rows[0].OriginalURL = "changed"
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 400 {
		t.Fatalf("changed choices %d %s", rec.Code, rec.Body)
	}
	before := f.calls
	rec = do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken))
	if rec.Code != 400 || f.calls != before {
		t.Fatalf("scope %d", rec.Code)
	}
	rec = do(t, h, "POST", path+"/apply", `{"original_url":"source","type":"poster","provider_id":"p"}`, bearer(memberToken))
	if rec.Code != 403 || f.calls != before {
		t.Fatalf("auth %d", rec.Code)
	}
	rec = do(t, h, "POST", path+"/apply", `{"original_url":"source","type":"poster","provider_id":"p"}`, bearer(adminToken))
	if rec.Code != 200 || f.request.OriginalURL != "source" || !strings.Contains(rec.Body.String(), `"revision":"revision"`) {
		t.Fatalf("apply %d %s", rec.Code, rec.Body)
	}
	before = f.calls
	rec = do(t, h, "POST", path+"/apply", `{"original_url":"","type":"poster"}`, bearer(adminToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid %d", rec.Code)
	}
	f.rows = nil
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty %d %s", rec.Code, rec.Body)
	}
}
func adminCatalogImagesFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_item_images", operationID: "listAdminItemImages", method: "GET", path: Prefix + "/admin/items/item-1/images", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminImagesPage", assertHeaders: []string{"Content-Type"}, scenario: "Administrator image choices use a bounded collection with current selection."},
		{name: "admin_item_image_apply", operationID: "applyAdminItemImage", method: "POST", path: Prefix + "/admin/items/item-1/images/apply", body: `{"original_url":"source","type":"poster"}`, headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminImageApplied", assertHeaders: []string{"Content-Type"}, scenario: "Synchronous artwork publication returns the immutable revision."},
	}
}
