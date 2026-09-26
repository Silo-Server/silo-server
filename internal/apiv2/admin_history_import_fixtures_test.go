package apiv2

func adminHistoryImportFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_history_source_canonical", operationID: "getAdminHistoryImportSource", scenario: "Canonical source configuration with its strong editor tag and no saved credential.", method: "GET", path: "/api/v2/admin/history-import-sources/1", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminHistoryImportSource"},
		{name: "admin_history_mapping_canonical", operationID: "getAdminHistoryImportMapping", scenario: "Canonical mapping configuration excludes enriched labels and import bookkeeping.", method: "GET", path: "/api/v2/admin/history-imports/mappings/2", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminHistoryImportMapping"},
		{name: "admin_history_source_precondition_required", operationID: "updateAdminHistoryImportSource", scenario: "An editor must supply its captured source tag before an update.", method: "PUT", path: "/api/v2/admin/history-import-sources/1", body: `{"name":"Edited"}`, headers: actingRequestAdmin, status: 428, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Problem"},
		{name: "admin_history_run_accepted", operationID: "createAdminHistoryImportRun", scenario: "Durable administrative run acceptance identifies its canonical monitor and polling delay.", method: "POST", path: "/api/v2/admin/history-imports/mappings/2/run", headers: actingRequestAdmin, status: 202, assertHeaders: []string{"Content-Type", "Cache-Control", "Location", "Retry-After"}, schema: "#/components/schemas/AdminHistoryImportRun"},
		{name: "admin_history_token_cleared", operationID: "clearAdminHistoryImportToken", scenario: "Clearing a source token is guarded and returns no body or validator.", method: "DELETE", path: "/api/v2/admin/history-imports/sources/1/token", headers: with(actingRequestAdmin, "If-Match", "*"), status: 204, assertHeaders: []string{"Cache-Control"}},
	}
}
