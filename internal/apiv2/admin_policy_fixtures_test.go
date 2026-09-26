package apiv2

func adminPolicyFixtureCases() []fixtureCase {
	const base = Prefix + "/admin/policy"
	const schema = "#/components/schemas/"
	get := func(name, operation, path, bodySchema, scenario string) fixtureCase {
		return fixtureCase{name: name, operationID: operation, method: "GET", path: base + path, headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: schema + bodySchema, scenario: scenario}
	}
	canonical := get("admin_policy_document", "getAdminPolicyDocument", "/documents/1", "AdminPolicySnapshot", "Canonical document with a strong revision validator and string identifiers.")
	canonical.assertHeaders = append(canonical.assertHeaders, "ETag")
	cursors := NewCursors([]byte("fixture-cursor-key"))
	docCursor, _ := cursors.Encode(CursorScope{OperationID: "listAdminPolicyDocuments", Security: "2/", Sort: "id", Tiebreaker: "id"}, int64(1))
	versionCursor, _ := cursors.Encode(CursorScope{OperationID: "listAdminPolicyVersions", Security: "2/", Filter: "1", Sort: "-version_number", Tiebreaker: "version_number"}, 1)
	return []fixtureCase{
		get("admin_policy_vendor", "listAdminPolicyVendor", "/vendor", "CollectionAdminPolicyVendor", "Vendor source modules use a named collection envelope."),
		get("admin_policy_documents", "listAdminPolicyDocuments", "/documents", "CollectionAdminPolicyDocument", "Saved document identities use string identifiers and UTC millisecond timestamps."),
		canonical,
		get("admin_policy_versions", "listAdminPolicyVersions", "/documents/1/versions", "CollectionAdminPolicyVersion", "Immutable version metadata omits source bodies."),
		get("admin_policy_version", "getAdminPolicyVersion", "/documents/1/versions/2", "AdminPolicyVersion", "The version path names the saved version ID rather than its ordinal."),
		get("admin_policy_decisions", "listAdminPolicyDecisions", "/decisions", "CollectionAdminPolicyDecision", "Empty decision pages carry items and terminal cursor state."),
		get("admin_policy_decision", "getAdminPolicyDecision", "/decisions/3", "AdminPolicyDecision", "A decision detail includes retained policy JSON samples."),
		{name: "admin_policy_create", operationID: "createAdminPolicyDocument", method: "POST", path: base + "/documents", headers: bearer(adminToken), body: `{"domain":"scope","name":"Fixture scope"}`, status: 201, assertHeaders: []string{"Content-Type", "Location", "ETag"}, schema: schema + "AdminPolicyDocument", scenario: "A created document returns its saved identity, location, and validator."},
		{name: "admin_policy_invalid_draft_saved", operationID: "createAdminPolicyVersion", method: "POST", path: base + "/documents/1/versions", headers: bearer(adminToken), body: `{"source":"invalid"}`, status: 201, assertHeaders: []string{"Content-Type", "Location"}, schema: schema + "AdminPolicyVersion", scenario: "A draft that fails compilation is still saved; 201 returns its ID and diagnostics."},
		{name: "admin_policy_enable_apply_failed", operationID: "setAdminPolicyEnabled", method: "PATCH", path: base + "/documents/1", headers: with(bearer(adminToken), "If-Match", "*"), body: `{"enabled":true}`, status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: schema + "AdminPolicyApplyResult", scenario: "A committed enable write reports its persisted generation and failed local application and publication without inviting replay."},
		{name: "admin_policy_activate_apply_failed", operationID: "activateAdminPolicyVersion", method: "PUT", path: base + "/documents/1/active-version", headers: with(bearer(adminToken), "If-Match", "*"), body: `{"version_id":"2"}`, status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: schema + "AdminPolicyApplyResult", scenario: "Activation reports persistence separately from local application; it is not an asynchronous job."},
		{name: "admin_policy_validate", operationID: "validateAdminPolicy", method: "POST", path: base + "/validate", headers: bearer(adminToken), body: `{"domain":"scope","source":"invalid"}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: schema + "AdminPolicyValidation", scenario: "Validation returns compile diagnostics without saving a version."},
		{name: "admin_policy_simulate", operationID: "simulateAdminPolicy", method: "POST", path: base + "/simulate", headers: bearer(adminToken), body: `{"domain":"permission","input":{"resource":"fixture"}}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: schema + "AdminPolicySimulationResult", scenario: "Simulation returns policy-owned JSON with evaluation time and observed generation."},
		{name: "admin_policy_missing_guard", operationID: "setAdminPolicyEnabled", method: "PATCH", path: base + "/documents/1", headers: bearer(adminToken), body: `{"enabled":false}`, status: 428, assertHeaders: []string{"Content-Type"}, schema: schema + "Problem", scenario: "The enabled mutation requires a captured canonical validator."},
		{name: "admin_policy_stale_guard", operationID: "activateAdminPolicyVersion", method: "PUT", path: base + "/documents/1/active-version", headers: with(bearer(adminToken), "If-Match", `"stale"`), body: `{"version_id":"2"}`, status: 412, assertHeaders: []string{"Content-Type", "ETag"}, schema: schema + "Problem", scenario: "A stale activation is rejected before persistence and returns the current validator."},
		{name: "admin_policy_null_enabled", operationID: "setAdminPolicyEnabled", method: "PATCH", path: base + "/documents/1", headers: with(bearer(adminToken), "If-Match", "*"), body: `{"enabled":null}`, status: 422, assertHeaders: []string{"Content-Type"}, schema: schema + "Problem", scenario: "Explicit null cannot silently disable a policy document."},
		{name: "admin_policy_delete_stale", operationID: "deleteAdminPolicyDocument", method: "DELETE", path: base + "/documents/1", headers: with(bearer(adminToken), "If-Match", `"stale"`), status: 412, assertHeaders: []string{"Content-Type", "ETag"}, schema: schema + "Problem", scenario: "A stale deletion fails before active-version constraints or persistence."},
		get("admin_policy_documents_continuation", "listAdminPolicyDocuments", "/documents?limit=2&cursor="+docCursor, "CollectionAdminPolicyDocument", "A valid signed document continuation returns explicit terminal page state."),
		get("admin_policy_versions_continuation", "listAdminPolicyVersions", "/documents/1/versions?limit=2&cursor="+versionCursor, "CollectionAdminPolicyVersion", "A valid version continuation bound to document 1 returns terminal page state."),
		{name: "admin_policy_version_cursor_mismatch", operationID: "listAdminPolicyVersions", method: "GET", path: base + "/documents/99/versions?cursor=" + versionCursor, headers: bearer(adminToken), status: 400, assertHeaders: []string{"Content-Type"}, schema: schema + "Problem", scenario: "A signed version continuation cannot be used for another document."},
	}
}
