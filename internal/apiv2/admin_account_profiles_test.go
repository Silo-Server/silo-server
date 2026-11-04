package apiv2

import (
	"net/http"
	"testing"
)

const adminProfilesBase = Prefix + "/admin/users/7/profiles"

func TestAdminProfilesRequireAnAdministrator(t *testing.T) {
	profiles := &fakeProfiles{view: fixtureProfileView()}
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	deps.Profiles = profiles
	h := NewHandler(deps)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, adminProfilesBase + "/p-owner", ""},
		{http.MethodPost, adminProfilesBase, `{"name":"Guest"}`},
		{http.MethodPatch, adminProfilesBase + "/p-owner", `{"pin":null}`},
		{http.MethodDelete, adminProfilesBase + "/p-owner", ""},
	} {
		requireProblem(t, do(t, h, c.method, c.path, c.body, bearer(memberToken)), TypePermissionDenied)
	}
	if profiles.last != nil || profiles.lastCreate != nil || profiles.lastDelete != nil {
		t.Fatal("a member reached the profile service")
	}
}

// Each write reaches the service for the addressed account, as an
// administrator rather than as a household member, so the household-manager
// check is the one it skips.
func TestAdminProfilesActOnTheAddressedAccount(t *testing.T) {
	profiles := &fakeProfiles{view: fixtureProfileView()}
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	deps.Profiles = profiles
	deps.Libraries = fakeLibraries{known: []int{3}}
	h := NewHandler(deps)

	if rec := do(t, h, http.MethodPatch, adminProfilesBase+"/p-owner", `{"pin":null}`, actingRequestAdmin); rec.Code != http.StatusOK {
		t.Fatalf("clear PIN: %d %s", rec.Code, rec.Body)
	}
	if c := profiles.last; c == nil || c.UserID != 7 || c.ProfileID != "p-owner" || !c.AccountAdmin || c.ActiveProfileID != "" || c.Request.PIN == nil || *c.Request.PIN != "" {
		t.Fatalf("clear PIN command = %+v", c)
	}
	if rec := do(t, h, http.MethodPatch, adminProfilesBase+"/p-owner", `{"pin":"2468"}`, actingRequestAdmin); rec.Code != http.StatusOK || *profiles.last.Request.PIN != "2468" {
		t.Fatalf("set PIN: %d %s", rec.Code, rec.Body)
	}
	requireProblem(t, do(t, h, http.MethodPatch, adminProfilesBase+"/p-owner", `{"pin":""}`, actingRequestAdmin), TypeValidationFailed)

	rec := do(t, h, http.MethodPost, adminProfilesBase, `{"name":"Guest","allowed_library_ids":["3"]}`, actingRequestAdmin)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != adminProfilesBase+"/p-new" {
		t.Fatalf("create: %d %s %q", rec.Code, rec.Body, rec.Header().Get("Location"))
	}
	if c := profiles.lastCreate; c.UserID != 7 || !c.AccountAdmin || c.ActiveProfileID != "" {
		t.Fatalf("create command = %+v", c)
	}

	if rec := do(t, h, http.MethodDelete, adminProfilesBase+"/p-owner", "", actingRequestAdmin); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if c := profiles.lastDelete; c.UserID != 7 || c.ProfileID != "p-owner" || !c.AccountAdmin {
		t.Fatalf("delete command = %+v", c)
	}

	requireProblem(t, do(t, h, http.MethodGet, adminProfilesBase+"/p-elsewhere", "", actingRequestAdmin), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/users/0/profiles/p-owner", "", actingRequestAdmin), TypeValidationFailed)
}
