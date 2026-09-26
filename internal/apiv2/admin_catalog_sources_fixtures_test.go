package apiv2

func adminCatalogSourcesFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_catalog_storage_sources", operationID: "listCatalogImportSources", method: "GET", path: Prefix + "/admin/catalog/import-sources", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminCatalogSource", assertHeaders: []string{"Content-Type"}, scenario: "A filtered storage page can be empty while retaining a signed continuation cursor."},
		{name: "admin_catalog_local_sources", operationID: "listLocalCatalogImportSources", method: "GET", path: Prefix + "/admin/catalog/local-import-sources?limit=1", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminCatalogSource", assertHeaders: []string{"Content-Type"}, scenario: "Local import files use bounded path-ordered pages."},
		{name: "admin_filesystem_browse", operationID: "browseAdminFilesystem", method: "GET", path: Prefix + "/admin/filesystem/browse?path=/catalog-seeds&limit=1", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminFilesystemPage", assertHeaders: []string{"Content-Type"}, scenario: "Directory browsing preserves the resolved path and parent with an explicit continuation page."},
	}
}
