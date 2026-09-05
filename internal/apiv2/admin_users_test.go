package apiv2

import (
	"encoding/json"
	"net/http"
	"net/url"
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
		Page  struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 2 || body.Page.HasMore || body.Page.NextCursor != "" {
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

type adminUserPage struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func decodeAdminUsers(t *testing.T, rec interface{ String() string }) adminUserPage {
	t.Helper()
	var page adminUserPage
	if err := json.Unmarshal([]byte(rec.String()), &page); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.String())
	}
	return page
}

// TestListAdminUsersCursor walks the whole account list one page at a time
// and checks the pages join with no gap and no repeat.
func TestListAdminUsersCursor(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	admin := bearer(adminToken)

	rec := do(t, h, http.MethodGet, "/api/v2/admin/users?limit=1", "", admin)
	first := decodeAdminUsers(t, rec.Body)
	if rec.Code != 200 || len(first.Items) != 1 || first.Items[0].ID != "1" || !first.Page.HasMore || first.Page.NextCursor == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/admin/users?limit=1&cursor="+url.QueryEscape(first.Page.NextCursor), "", admin)
	second := decodeAdminUsers(t, rec.Body)
	if rec.Code != 200 || len(second.Items) != 1 || second.Items[0].ID != "2" || second.Page.HasMore || second.Page.NextCursor != "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A cursor is bound to the acting account and to the operation's secret.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users?limit=1&cursor="+url.QueryEscape(first.Page.NextCursor), "", bearer(otherAdminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users?cursor=nonsense", "", admin), TypeInvalidCursor)
}

func TestListAdminUsersValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	for _, tc := range []struct{ query, location, code string }{
		{"offset=10", "query.offset", codeUnknownParameter},
		{"limit=0", "query.limit", codeOutOfRange},
		{"limit=201", "query.limit", codeOutOfRange},
	} {
		p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/admin/users?"+tc.query, "", bearer(adminToken)), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.query, p.Errors)
		}
	}
}
