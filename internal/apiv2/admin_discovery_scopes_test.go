package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDiscoveryScopesEnforceProjectionAndOwnerAuthority(t *testing.T) {
	deps := pilotDeps(nil, nil)
	sessions := new(fakeAdminPlaybackSessions)
	libraries := new(fakeUserLibraries)
	deps.AdminPlaybackSessions = sessions
	deps.UserLibraries = libraries
	users := map[int]*models.User{
		1: {ID: 1, Role: "user", Enabled: true},
		2: {ID: 2, Role: "admin", Enabled: true},
	}
	keys := fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_summary":        {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminSessionsSummaryRead}},
		"sa_member_summary": {ID: 2, UserID: 1, Scopes: []string{auth.ScopeAdminSessionsSummaryRead}},
		"sa_libraries":      {ID: 3, UserID: 1, Scopes: []string{auth.ScopeLibrariesRead}},
	}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, keys, fakeUsers{users: users})
	h := NewHandler(deps)
	path := Prefix + "/admin/sessions/summary"
	rec := do(t, h, "GET", path+"?user_id=7&limit=1", "", bearer("sa_summary"))
	var summary AdminPlaybackSummaryOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &summary.Body); err != nil || rec.Code != 200 || summary.Body.Count != 2 || len(summary.Body.Items) != 1 {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	for _, field := range []string{"session_id", "profile_id", "profile_name", "client_ip", "client_user_agent", "has_playback_control", "media_file_id"} {
		if strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Fatal("summary exposed diagnostic field", field)
		}
	}
	for _, query := range []string{"?user_id=0", "?limit=0", "?limit=101", "?cursor=anything"} {
		requireProblem(t, do(t, h, "GET", path+query, "", bearer("sa_summary")), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/sessions", "", bearer("sa_summary")), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path, "", bearer("sa_member_summary")), TypePermissionDenied)
	if sessions.calls != 1 {
		t.Fatal("denied request reached session loader", sessions.calls)
	}
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{12}}})
	h = NewHandler(deps)
	rec = do(t, h, "GET", Prefix+"/user/libraries", "", bearer("sa_libraries"))
	if rec.Code != 200 || libraries.userID != 1 || !libraries.scope.LibrariesRestricted || len(libraries.scope.AllowedLibraryIDs) != 1 {
		t.Fatal(rec.Code, rec.Body.String(), libraries.scope)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/libraries", "", bearer("sa_libraries")), TypePermissionDenied)
	requireProblem(t, do(t, h, "POST", Prefix+"/playback/start", `{}`, bearer("sa_libraries")), TypePermissionDenied)
}
