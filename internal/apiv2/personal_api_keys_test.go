package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

type fakePersonalAPIKeys struct {
	*fakeAdminAPIKeys
	account int
	deleted int64
	reads   int
}

func (f *fakePersonalAPIKeys) ListPersonalAPIKeysPage(_ context.Context, user int, after *auth.APIKeyPageKey, _ int) ([]handlers.APIKeyListItem, bool, error) {
	f.account = user
	f.after = after
	f.reads++
	row := f.row
	row.UserID = user
	return []handlers.APIKeyListItem{{APIKeyConfiguration: row}}, after == nil, nil
}
func (f *fakePersonalAPIKeys) RevokePersonalAPIKey(_ context.Context, user int, id int64) error {
	f.account = user
	f.deleted = id
	f.writes++
	if user != f.row.UserID || id != f.row.ID {
		return auth.ErrAPIKeyNotFound
	}
	return nil
}
func personalKeyHandler() (http.Handler, *fakePersonalAPIKeys) {
	f := &fakePersonalAPIKeys{fakeAdminAPIKeys: fixtureAdminAPIKeys()}
	deps := pilotDeps(nil, nil)
	deps.PersonalAPIKeys = f
	return NewHandler(deps), f
}
func TestPersonalAPIKeysAccountAndSecrets(t *testing.T) {
	h, f := personalKeyHandler()
	created := do(t, h, "POST", Prefix+"/api-keys", `{"label":"Script"}`, bearer(adminToken))
	if created.Code != 201 || f.createUser != 2 || !strings.Contains(created.Body.String(), `"key":"creation-secret"`) {
		t.Fatal(created.Code, created.Body.String(), f.createUser)
	}
	listed := do(t, h, "GET", Prefix+"/api-keys", "", bearer(adminToken))
	if listed.Code != 200 || f.account != 2 {
		t.Fatal(listed.Code, listed.Body.String(), f.account)
	}
	for _, secret := range []string{`"key":`, `"revision":`, `"username":`} {
		if strings.Contains(listed.Body.String(), secret) {
			t.Fatal(listed.Body.String())
		}
	}
	requireProblem(t, do(t, h, "DELETE", Prefix+"/api-keys/7", "", bearer(memberToken)), TypeNotFound)
	if f.account != 1 || f.deleted != 7 {
		t.Fatal(f.account, f.deleted)
	}
	deleted := do(t, h, "DELETE", Prefix+"/api-keys/7", "", bearer(adminToken))
	if deleted.Code != 204 {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
}
func TestPersonalAPIKeysCredentialPolicyAndValidation(t *testing.T) {
	h, f := personalKeyHandler()
	for _, op := range []struct{ method, path, body string }{
		{"GET", "/api-keys", ""}, {"POST", "/api-keys", `{"label":"Script"}`}, {"DELETE", "/api-keys/7", ""},
	} {
		requireProblem(t, do(t, h, op.method, Prefix+op.path, op.body, bearer(apiKeyToken)), TypePermissionDenied)
		requireProblem(t, do(t, h, op.method, Prefix+op.path, op.body, nil), TypeAuthenticationRequired)
	}
	for _, body := range []string{`{"label":"Script","scopes":null}`, `{"label":null}`, `{"label":"Script","user_id":"2"}`} {
		requireProblem(t, do(t, h, "POST", Prefix+"/api-keys", body, bearer(adminToken)), TypeValidationFailed)
	}
	if f.writes != 0 || f.reads != 0 {
		t.Fatal("rejected requests reached service")
	}
	// Scope discovery retains its v1 availability to API-key authentication.
	scopes := do(t, h, "GET", Prefix+"/api-keys/scopes", "", bearer(apiKeyToken))
	if scopes.Code != 200 {
		t.Fatal(scopes.Code, scopes.Body.String())
	}
	deps := pilotDeps(nil, nil)
	absent := NewHandler(deps)
	requireProblem(t, do(t, absent, "GET", Prefix+"/api-keys", "", bearer(memberToken)), TypeDependencyUnavailable)
	scopes = do(t, absent, "GET", Prefix+"/api-keys/scopes", "", bearer(memberToken))
	if scopes.Code != 200 || !strings.Contains(scopes.Body.String(), `"available":false`) {
		t.Fatal(scopes.Code, scopes.Body.String())
	}
}

// TestPersonalAPIKeysCreationRequiresAdmin covers #1361: a regular account's
// login session must not mint an API key through the personal route, while
// listing and revoking the keys it already owns keep working.
func TestPersonalAPIKeysCreationRequiresAdmin(t *testing.T) {
	h, f := personalKeyHandler()
	requireProblem(t, do(t, h, "POST", Prefix+"/api-keys", `{"label":"Script","scopes":[]}`, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "POST", Prefix+"/api-keys", `{"label":"Script"}`, bearer(impersonatedToken)), TypePermissionDenied)
	if f.writes != 0 {
		t.Fatal("a non-admin create reached the service")
	}
	if listed := do(t, h, "GET", Prefix+"/api-keys", "", bearer(memberToken)); listed.Code != 200 {
		t.Fatal(listed.Code, listed.Body.String())
	}
	f.row.UserID = 1
	if deleted := do(t, h, "DELETE", Prefix+"/api-keys/7", "", bearer(memberToken)); deleted.Code != 204 {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
}

func TestPersonalAPIKeysCursorIsolation(t *testing.T) {
	h, f := personalKeyHandler()
	listed := do(t, h, "GET", Prefix+"/api-keys?limit=1", "", bearer(memberToken))
	var page Collection[PersonalAPIKeyListItem]
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page == nil || page.Page.NextCursor == "" {
		t.Fatal(listed.Body.String())
	}
	cursor := page.Page.NextCursor
	path := Prefix + "/api-keys?limit=1&cursor=" + url.QueryEscape(cursor)
	requireProblem(t, do(t, h, "GET", path, "", bearer(adminToken)), TypeInvalidCursor)
	if f.reads != 1 {
		t.Fatal("cross-account cursor reached storage")
	}
	next := do(t, h, "GET", path, "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
	if next.Code != 200 || f.after == nil || f.after.ID != 7 {
		t.Fatal(next.Code, next.Body.String())
	}
}
