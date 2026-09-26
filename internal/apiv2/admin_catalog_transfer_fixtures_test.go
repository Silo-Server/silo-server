package apiv2

func adminCatalogTransferFixtureCases() []fixtureCase {
	const importBody = `{"local_path":"/seed.json.gz","conflict_mode":"skip_existing","path_rewrites":[]}`
	return []fixtureCase{
		{name: "admin_catalog_export_queued", operationID: "createCatalogExportJob", method: "POST", path: Prefix + "/admin/catalog/export-jobs", headers: bearer(adminToken), body: `{}`, status: 202, schema: "#/components/schemas/AdminTaskJob", assertHeaders: []string{"Content-Type", "Location", "Retry-After"}, scenario: "HTTP 202 acknowledges the persisted export job."},
		{name: "admin_catalog_import_queued", operationID: "createCatalogImportJob", method: "POST", path: Prefix + "/admin/catalog/import-jobs", headers: bearer(adminToken), body: importBody, status: 202, schema: "#/components/schemas/AdminTaskJob", assertHeaders: []string{"Content-Type", "Location", "Retry-After"}, scenario: "A queued import retains a source reference, not frozen source bytes."},
		{name: "admin_catalog_import_committed", operationID: "importAdminCatalog", method: "POST", path: Prefix + "/admin/catalog/import", headers: bearer(adminToken), body: importBody, status: 200, schema: "#/components/schemas/ImportResult", assertHeaders: []string{"Content-Type"}, scenario: "Synchronous success reports committed import counts."},
		{name: "admin_catalog_export_link", operationID: "publishCatalogExportJob", method: "POST", path: Prefix + "/admin/catalog/export-jobs/catalog-job/publish", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminCatalogPublished", assertHeaders: []string{"Content-Type"}, scenario: "A saved signed URL retains its original seven-day expiry."},
		{name: "admin_catalog_search_status", operationID: "getAdminCatalogSearchStatus", method: "GET", path: Prefix + "/admin/catalog/search/status", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminCatalogSearchRuntimeStatus", assertHeaders: []string{"Content-Type"}, scenario: "Search runtime status preserves large event identities as strings."},
		{name: "admin_catalog_export_media_refused", operationID: opExportAdminCatalog, method: "POST", path: Prefix + "/admin/catalog/export", headers: with(bearer(adminToken), "Accept", "application/json"), body: `{}`, status: 406, schema: "#/components/schemas/Problem", assertHeaders: []string{"Content-Type"}, scenario: "A synchronous gzip export refuses a JSON-only Accept header before execution; binary success is covered by actual-router byte tests."},
	}
}
