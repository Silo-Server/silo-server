package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminAPIKeys struct {
	row        handlers.APIKeyConfiguration
	guard      auth.APIKeyPrecondition
	writes     int
	race       bool
	after      *auth.APIKeyPageKey
	createUser int
	err        error
}

func fixtureAdminAPIKeys() *fakeAdminAPIKeys {
	return &fakeAdminAPIKeys{row: handlers.APIKeyConfiguration{ID: 7, UserID: 2, Label: "Automation", KeyPrefix: "sa_12345678", RateTier: adminAPIKeyStandardTier, Scopes: []string{}, CreatedAt: fixedTime(), Revision: 11}}
}
func (f *fakeAdminAPIKeys) GetAdminAPIKey(context.Context, int64) (*handlers.APIKeyConfiguration, error) {
	row := f.row
	return &row, f.err
}
func (f *fakeAdminAPIKeys) ListAdminAPIKeysPage(_ context.Context, after *auth.APIKeyPageKey, _ int) ([]handlers.AdminAPIKeyListItem, bool, error) {
	f.after = after
	row := f.row
	if after != nil {
		row.ID = 6
	}
	return []handlers.AdminAPIKeyListItem{{APIKeyListItem: handlers.APIKeyListItem{APIKeyConfiguration: row, LastUsedAt: new(fixedTime())}, Username: "owner"}}, after == nil, f.err
}
func (f *fakeAdminAPIKeys) CreateAdminAPIKey(_ context.Context, user int, label string, scopes []string) (*models.APIKey, error) {
	f.createUser = user
	f.writes++
	return &models.APIKey{ID: 8, UserID: user, Label: label, Scopes: scopes, Key: "creation-secret", RateTier: adminAPIKeyStandardTier, CreatedAt: fixedTime()}, f.err
}
func (f *fakeAdminAPIKeys) UpdateAdminAPIKeyTier(_ context.Context, _ int64, tier string, guard auth.APIKeyPrecondition) (*handlers.APIKeyConfiguration, error) {
	f.guard = guard
	if f.race {
		f.row.Revision++
		return nil, &auth.APIKeyRevisionConflict{Current: &models.APIKeyMetadata{ID: f.row.ID, Revision: f.row.Revision}}
	}
	f.writes++
	f.row.Revision++
	f.row.RateTier = tier
	row := f.row
	return &row, f.err
}
func (f *fakeAdminAPIKeys) DeleteAdminAPIKey(_ context.Context, _ int64, guard auth.APIKeyPrecondition) error {
	f.guard = guard
	f.writes++
	return f.err
}
func adminAPIKeyHandler(f *fakeAdminAPIKeys) http.Handler {
	deps := requestDeps(fixtureRequests())
	if f != nil {
		deps.AdminAPIKeys = f
	}
	return NewHandler(deps)
}

func TestAdminAPIKeyConditionalMetadata(t *testing.T) {
	f := fixtureAdminAPIKeys()
	h := adminAPIKeyHandler(f)
	path := Prefix + adminAPIKeyPath + "/7"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	for _, field := range []string{`"key":`, `"revision":`, `"username":`, `"last_used_at":`} {
		if strings.Contains(read.Body.String(), field) {
			t.Fatalf("canonical contains %s: %s", field, read.Body.String())
		}
	}
	cached := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", "W/"+tag))
	if cached.Code != 304 || cached.Body.Len() != 0 || cached.Header().Get("ETag") != tag {
		t.Fatal(cached.Code, cached.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(with(actingRequestAdmin, "If-Match", `"stale"`), "If-None-Match", tag)), TypePreconditionFailed)
	body := `{"rate_tier":"elevated"}`
	requireProblem(t, do(t, h, http.MethodPut, path+"/tier", body, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path+"/tier", body, with(actingRequestAdmin, "If-Match", "W/"+tag)), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodPut, path+"/tier", body, with(with(actingRequestAdmin, "If-Match", tag), "If-None-Match", tag)), TypePreconditionFailed)
	if f.writes != 0 {
		t.Fatal("preconditions reached mutation")
	}
	saved := do(t, h, http.MethodPut, path+"/tier", body, with(actingRequestAdmin, "If-Match", `"other", `+tag))
	if saved.Code != 200 || f.guard.Revision != 11 || f.guard.Any || saved.Header().Get("ETag") == tag {
		t.Fatal(saved.Code, saved.Body.String(), f.guard)
	}
	tag = saved.Header().Get("ETag")
	f.race = true
	raced := do(t, h, http.MethodPut, path+"/tier", body, with(actingRequestAdmin, "If-Match", tag))
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") == tag || raced.Header().Get("ETag") == "" || strings.Contains(raced.Body.String(), "key_prefix") {
		t.Fatal(raced.Header(), raced.Body.String())
	}
	f.race = false
	wildcard := do(t, h, http.MethodPut, path+"/tier", body, with(actingRequestAdmin, "If-Match", "*"))
	if wildcard.Code != 200 || !f.guard.Any || f.guard.Revision != 0 {
		t.Fatal(wildcard.Code, f.guard)
	}
	deleted := do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", wildcard.Header().Get("ETag")))
	if deleted.Code != 204 || deleted.Body.Len() != 0 || deleted.Header().Get("ETag") != "" {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
}
func TestAdminAPIKeyListCursorAndCreate(t *testing.T) {
	f := fixtureAdminAPIKeys()
	h := adminAPIKeyHandler(f)
	path := Prefix + adminAPIKeyPath
	first := do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
	if first.Code != 200 || first.Header().Get("ETag") != "" {
		t.Fatal(first.Code, first.Body.String())
	}
	var page Collection[AdminAPIKeyListItem]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Page.HasMore || page.Page.NextCursor == "" || page.Items[0].LastUsedAt == nil {
		t.Fatal(first.Body.String())
	}
	next := do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", actingRequestAdmin)
	if next.Code != 200 || f.after == nil || f.after.ID != 7 || !f.after.CreatedAt.Equal(fixedTime()) {
		t.Fatal(next.Code, next.Body.String(), f.after)
	}
	requireProblem(t, do(t, h, http.MethodGet, path+"?limit=2&cursor="+page.Page.NextCursor, "", actingRequestAdmin), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, path+"?limit=201", "", actingRequestAdmin), TypeValidationFailed)
	created := do(t, h, http.MethodPost, path, `{"label":"Tool"}`, actingRequestAdmin)
	if created.Code != 201 || created.Header().Get("Location") != path+"/8" || created.Header().Get("ETag") != "" || !strings.Contains(created.Body.String(), `"key":"creation-secret"`) || f.createUser != 2 {
		t.Fatal(created.Code, created.Body.String(), f.createUser)
	}
	created = do(t, h, http.MethodPost, path, `{"label":"Tool","user_id":"3"}`, actingRequestAdmin)
	if created.Code != 201 || f.createUser != 3 {
		t.Fatal(created.Code, created.Body.String())
	}
	f.err = errors.New("private database credential")
	failed := do(t, h, http.MethodPost, path, `{"label":"Tool"}`, actingRequestAdmin)
	if failed.Code != 500 || strings.Contains(failed.Body.String(), "private") {
		t.Fatal(failed.Code, failed.Body.String())
	}
}
func TestAdminAPIKeyAuthorityAndUnavailable(t *testing.T) {
	h := adminAPIKeyHandler(nil)
	path := Prefix + adminAPIKeyPath
	caps := do(t, h, http.MethodGet, path+"/capabilities", "", actingRequestAdmin)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"available":false`) {
		t.Fatal(caps.Code, caps.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
}

func TestAdminAPIKeyValidatorsBindAuthority(t *testing.T) {
	f := fixtureAdminAPIKeys()
	deps := requestDeps(fixtureRequests())
	deps.AdminAPIKeys = f
	h := NewHandler(deps)
	path := Prefix + adminAPIKeyPath
	first := do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
	var page Collection[AdminAPIKeyListItem]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	read := do(t, h, http.MethodGet, path+"/7", "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	for _, headers := range []map[string]string{bearer(adminToken), bearer(otherAdminToken), with(with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "t")} {
		requireProblem(t, do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", headers), TypeInvalidCursor)
		requireProblem(t, do(t, h, http.MethodPut, path+"/7/tier", `{"rate_tier":"elevated"}`, with(headers, "If-Match", tag)), TypePreconditionFailed)
	}
	if f.writes != 0 {
		t.Fatal("another authority applied captured draft")
	}
}

func TestAdminAPIKeyAuthenticationPolicy(t *testing.T) {
	for _, credential := range []struct {
		name    string
		user    int
		scopes  []string
		allowed bool
	}{
		{"unscoped administrator", 2, nil, true},
		{"scoped administrator", 2, []string{auth.ScopeAdminUsers}, false},
		{"ordinary account", 1, nil, false},
	} {
		t.Run(credential.name, func(t *testing.T) {
			f := fixtureAdminAPIKeys()
			deps := requestDeps(fixtureRequests())
			deps.AdminAPIKeys = f
			deps.Auth = apimw.NewAuthMiddleware(nil, nil, fakeAPIKeys{keys: map[string]*models.APIKey{apiKeyToken: {ID: 9, UserID: credential.user, Scopes: credential.scopes}}}, fakeUsers{users: map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}})
			h := NewHandler(deps)
			for _, op := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, adminAPIKeyPath + "/capabilities", "", 200},
				{http.MethodGet, adminAPIKeyPath, "", 200},
				{http.MethodGet, adminAPIKeyPath + "/7", "", 200},
				{http.MethodPost, adminAPIKeyPath, `{"label":"Tool"}`, 201},
				{http.MethodPut, adminAPIKeyPath + "/7/tier", `{"rate_tier":"elevated"}`, 200},
				{http.MethodDelete, adminAPIKeyPath + "/7", "", 204},
			} {
				got := do(t, h, op.method, Prefix+op.path, op.body, with(bearer(apiKeyToken), "If-Match", "*"))
				if credential.allowed {
					if got.Code != op.status {
						t.Fatalf("%s %s: %d %s", op.method, op.path, got.Code, got.Body.String())
					}
				} else if got.Code != http.StatusForbidden {
					t.Fatalf("%s %s admitted: %d %s", op.method, op.path, got.Code, got.Body.String())
				}
			}
			if !credential.allowed && f.writes != 0 {
				t.Fatal("denied credential reached mutations")
			}
		})
	}
}

func TestAdminAPIKeyCreationRejectsExplicitNulls(t *testing.T) {
	for _, body := range []string{
		`{"label":"Review","user_id":null}`,
		`{"label":"Review","scopes":null}`,
		`{"label":"Review","scopes":[null]}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := fixtureAdminAPIKeys()
			requireProblem(t, do(t, adminAPIKeyHandler(f), http.MethodPost, Prefix+adminAPIKeyPath, body, actingRequestAdmin), TypeValidationFailed)
			if f.writes != 0 {
				t.Fatal("explicit null reached creation service")
			}
		})
	}
	f := fixtureAdminAPIKeys()
	response := do(t, adminAPIKeyHandler(f), http.MethodPost, Prefix+adminAPIKeyPath, `{"label":"Review"}`, actingRequestAdmin)
	if response.Code != http.StatusCreated || f.writes != 1 || f.createUser != 2 {
		t.Fatal("omission defaults changed", response.Code, response.Body.String())
	}
	var created AdminAPIKeyCreated
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Scopes == nil || len(created.Scopes) != 0 {
		t.Fatal("omitted scopes must produce an empty array")
	}
}
