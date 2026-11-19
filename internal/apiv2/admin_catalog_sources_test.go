package apiv2

import (
	"context"

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
