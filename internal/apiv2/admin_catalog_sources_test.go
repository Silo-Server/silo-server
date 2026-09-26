package apiv2

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminCatalogSources struct {
	calls, limit  int
	after, prefix string
}

func (f *fakeAdminCatalogSources) ListCatalogImportSourcesPage(_ context.Context, after string, limit int) ([]handlers.CatalogImportSource, string, error) {
	f.calls++
	f.after, f.limit = after, limit
	if after == "" {
		return []handlers.CatalogImportSource{}, "opaque-storage-token", nil
	}
	return []handlers.CatalogImportSource{{Key: "catalog-seeds/z.json.gz", SizeBytes: 12}}, "", nil
}
func (f *fakeAdminCatalogSources) ListLocalCatalogImportSourcesPage(_ context.Context, after string, limit int) ([]handlers.CatalogImportSource, error) {
	f.calls++
	f.after, f.limit = after, limit
	rows := []handlers.CatalogImportSource{}
	for _, key := range []string{"/catalog-seeds/a.json.gz", "/catalog-seeds/b.json.gz"} {
		if key > after && len(rows) < limit {
			rows = append(rows, handlers.CatalogImportSource{Key: key})
		}
	}
	return rows, nil
}
func (f *fakeAdminCatalogSources) BrowseDirectoryPage(ctx context.Context, path, prefix, after string, limit int) (handlers.FilesystemDirectoryPage, error) {
	f.prefix = prefix
	rows, err := f.ListLocalCatalogImportSourcesPage(ctx, after, limit)
	return handlers.FilesystemDirectoryPage{Path: path, Parent: "/", Entries: rows}, err
}

func TestAdminCatalogSourcesCursorBindingAndEmptyStoragePage(t *testing.T) {
	f := &fakeAdminCatalogSources{}
	deps, _ := libraryDeps(t)
	deps.AdminCatalogSources = f
	deps.AdminFilesystem = f
	h := newTestHandler(t, deps)
	base := Prefix + "/admin/catalog/import-sources"
	first := do(t, h, "GET", base+"?limit=1", "", bearer(adminToken))
	if first.Code != 200 {
		t.Fatalf("%d %s", first.Code, first.Body)
	}
	var page Collection[AdminCatalogSource]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Page == nil || !page.Page.HasMore || f.limit != 1 {
		t.Fatalf("page: %#v, limit=%d", page, f.limit)
	}
	cursor := url.QueryEscape(page.Page.NextCursor)
	second := do(t, h, "GET", base+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if second.Code != 200 || f.after != "opaque-storage-token" {
		t.Fatalf("continuation: %d %s, after=%q", second.Code, second.Body, f.after)
	}
	before := f.calls
	wrong := do(t, h, "GET", Prefix+"/admin/catalog/local-import-sources?cursor="+cursor, "", bearer(adminToken))
	if wrong.Code < 400 || f.calls != before {
		t.Fatalf("cross-operation cursor accepted: %d", wrong.Code)
	}
	for _, path := range []string{base, Prefix + "/admin/catalog/local-import-sources", Prefix + "/admin/filesystem/browse"} {
		denied := do(t, h, "GET", path, "", bearer(memberToken))
		if denied.Code != 403 || f.calls != before {
			t.Fatalf("profile reached admin source: %d %s", denied.Code, denied.Body)
		}
	}
}

func TestAdminFilesystemCursorBindsPathAndPrefix(t *testing.T) {
	f := &fakeAdminCatalogSources{}
	deps, _ := libraryDeps(t)
	deps.AdminFilesystem = f
	h := newTestHandler(t, deps)
	base := Prefix + "/admin/filesystem/browse?path=/catalog-seeds&name_prefix=a&limit=1"
	first := do(t, h, "GET", base, "", bearer(adminToken))
	if first.Code != 200 {
		t.Fatalf("%d %s", first.Code, first.Body)
	}
	var page AdminFilesystemPage
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if f.prefix != "a" || f.limit != 2 || len(page.Items) != 1 || page.Page == nil || !page.Page.HasMore {
		t.Fatalf("page: %#v", page)
	}
	before := f.calls
	for _, query := range []string{"path=/different&name_prefix=a", "path=/catalog-seeds&name_prefix=b"} {
		bad := do(t, h, "GET", Prefix+"/admin/filesystem/browse?"+query+"&cursor="+url.QueryEscape(page.Page.NextCursor), "", bearer(adminToken))
		if bad.Code < 400 || f.calls != before {
			t.Fatalf("changed filter reached service: %d", bad.Code)
		}
	}
}
