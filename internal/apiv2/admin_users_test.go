package apiv2

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestListAdminUsers(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, "/api/v2/admin/users", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
		Page  json.RawMessage              `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 2 || body.Page != nil {
		t.Fatalf("body = %s", rec.Body.String())
	}
	first, second := body.Items[0], body.Items[1]
	for field, want := range map[string]string{
		"id": `"1"`, "role": `"user"`, "permissions": `[]`, "library_ids": `null`, "max_playback_quality": `null`,
		"max_streams": `2`, "transcode_allowed": `null`, "download_allowed": `true`, "access_group_id": `"2"`,
		"created_at": `"2026-01-02T03:04:05.678Z"`, "last_active_at": `"2026-01-02T03:04:05.678Z"`,
	} {
		if string(first[field]) != want {
			t.Errorf("%s = %s, want %s", field, first[field], want)
		}
	}
	if string(second["library_ids"]) != `[]` || string(second["access_group_id"]) != `null` || string(second["last_active_at"]) != `null` || string(second["role"]) != `"admin"` {
		t.Errorf("second = %s", rec.Body.String())
	}
	var policy struct {
		LibraryIDs  []string `json:"library_ids"`
		Permissions []string `json:"permissions"`
		MaxStreams  int      `json:"max_streams"`
	}
	if err := json.Unmarshal(first["effective_policy"], &policy); err != nil || len(policy.LibraryIDs) != 1 || policy.LibraryIDs[0] != "3" || policy.Permissions == nil || policy.MaxStreams != 2 {
		t.Errorf("effective_policy = %s", first["effective_policy"])
	}
	// The admin's primary profile is accepted; an absent header too.
	if rec := do(t, h, http.MethodGet, "/api/v2/admin/users", "", with(bearer(adminToken), "X-Profile-Id", "p-primary")); rec.Code != 200 {
		t.Fatalf("primary profile: %d %s", rec.Code, rec.Body.String())
	}
}

func TestListAdminUsersDenied(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users", "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users", "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users?role=admin", "", bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.role" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	deps := pilotDeps(nil, nil)
	deps.AdminUsers = fakeAdminUsers{err: errStore}
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/admin/users", "", bearer(adminToken)), TypeInternalError)
	deps.AdminUsers = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/admin/users", "", bearer(adminToken)), TypeDependencyUnavailable)
}
