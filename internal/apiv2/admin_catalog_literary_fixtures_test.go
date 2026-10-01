package apiv2

func adminCatalogLiteraryFixtureCases() []fixtureCase {
	const decision = `{"source_content_id":"ebook-1","target_content_id":"audio-1"}`
	return []fixtureCase{
		{name: "admin_literary_candidates", operationID: "listAdminLiteraryCandidates", method: "GET", path: Prefix + "/admin/literary-works/items/ebook-1/candidates", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminLiteraryCandidates", scenario: "Bounded ranked candidates retain typed scoring evidence."},
		{name: "admin_literary_link", operationID: "linkAdminLiteraryItems", method: "POST", path: Prefix + "/admin/literary-works/link", headers: bearer(adminToken), body: `{"content_ids":["ebook-1","audio-1"]}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminLiteraryLink", scenario: "Synchronous service success returns the selected work identity."},
		{name: "admin_literary_confirm", operationID: "confirmAdminLiteraryMatch", method: "POST", path: Prefix + "/admin/literary-works/matches/confirm", headers: bearer(adminToken), body: decision, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminLiteraryDecision", scenario: "Confirmation returns after linking and recording the account-attributed decision."},
		{name: "admin_literary_ignore", operationID: "ignoreAdminLiteraryMatch", method: "POST", path: Prefix + "/admin/literary-works/matches/ignore", headers: bearer(adminToken), body: decision, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminLiteraryDecision", scenario: "An ignored match has no returned work identity."},
		{name: "admin_literary_unlink", operationID: "unlinkAdminLiteraryItem", method: "DELETE", path: Prefix + "/admin/literary-works/work-1/items/ebook-1", headers: bearer(adminToken), status: 204, scenario: "Unlink success has no response body."},
	}
}
