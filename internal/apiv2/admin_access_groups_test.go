package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminAccessGroups struct {
	g       access.Group
	writes  int
	race    bool
	last    access.UpdateGroupInput
	created access.CreateGroupInput
	guard   access.GroupPrecondition
}

func fixtureAdminAccessGroups() *fakeAdminAccessGroups {
	return &fakeAdminAccessGroups{g: access.Group{ID: 7, Revision: 11, Name: "Group", MaxPlaybackQuality: "original", MemberCount: 3, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}}
}
func (f *fakeAdminAccessGroups) GetAdminAccessGroup(context.Context, int64) (*access.Group, error) {
	g := f.g
	return &g, nil
}
func (f *fakeAdminAccessGroups) ListAdminAccessGroupsPage(_ context.Context, after *access.GroupPageKey, _ int) ([]access.Group, bool, error) {
	g := f.g
	if after != nil {
		g.ID = 8
	}
	return []access.Group{g}, after == nil, nil
}
func (f *fakeAdminAccessGroups) CreateAdminAccessGroup(_ context.Context, in access.CreateGroupInput) (*access.Group, error) {
	f.created = in
	f.writes++
	g := f.g
	return &g, nil
}
func (f *fakeAdminAccessGroups) UpdateAdminAccessGroup(_ context.Context, _ int64, in access.UpdateGroupInput, guard access.GroupPrecondition) (*access.Group, error) {
	f.guard = guard
	if f.race {
		f.g.Revision++
		return nil, &access.GroupRevisionConflict{Current: &f.g}
	}
	f.writes++
	f.last = in
	f.g.Revision++
	g := f.g
	return &g, nil
}
func (f *fakeAdminAccessGroups) DeleteAdminAccessGroup(_ context.Context, _ int64, guard access.GroupPrecondition) error {
	f.guard = guard
	f.writes++
	return nil
}
func TestAdminAccessGroupTransport(t *testing.T) {
	f := fixtureAdminAccessGroups()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccessGroups = f
	h := NewHandler(deps)
	path := Prefix + "/admin/access-groups/7"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" || strings.Contains(read.Body.String(), "member_count") {
		t.Fatal(read.Code, read.Body.String())
	}
	cached := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", "W/"+tag))
	if cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatal(cached.Code, cached.Body.String())
	}
	body := `{"library_ids":[],"allowed_permissions":[],"download_allowed":false}`
	requireProblem(t, do(t, h, http.MethodPut, path, body, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	saved := do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", tag))
	if saved.Code != 200 || f.last.LibraryIDs == nil || *f.last.LibraryIDs == nil || f.last.DownloadAllowed == nil || *f.last.DownloadAllowed || f.guard.Revision != 11 {
		t.Fatal(saved.Code, saved.Body.String(), f.last)
	}
	f.race = true
	raced := do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", saved.Header().Get("ETag")))
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") == saved.Header().Get("ETag") {
		t.Fatal("conflict did not expose current tag")
	}
	f.race = false
	cleared := do(t, h, http.MethodPut, path, `{"library_ids":null,"allowed_permissions":null}`, with(actingRequestAdmin, "If-Match", "*"))
	if cleared.Code != 200 || f.last.LibraryIDs == nil || *f.last.LibraryIDs != nil || f.last.AllowedPermissions == nil || *f.last.AllowedPermissions != nil || !f.guard.Any {
		t.Fatal(cleared.Code, cleared.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{"download_allowed":null}`, with(actingRequestAdmin, "If-Match", "*")), TypeValidationFailed)
	deleted := do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", "*"))
	if deleted.Code != 204 || deleted.Body.Len() != 0 || deleted.Header().Get("ETag") != "" {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
	created := do(t, h, http.MethodPost, Prefix+"/admin/access-groups", `{"name":"New","download_allowed":false,"library_ids":[]}`, actingRequestAdmin)
	if created.Code != 201 || created.Header().Get("Location") != path || f.created.DownloadAllowed || !f.created.TranscodeAllowed || f.created.LibraryIDs == nil {
		t.Fatal(created.Code, created.Body.String(), f.created)
	}
}
func TestAdminAccessGroupPaginationAuthority(t *testing.T) {
	f := fixtureAdminAccessGroups()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccessGroups = f
	h := NewHandler(deps)
	path := Prefix + "/admin/access-groups"
	first := do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
	var page Collection[AdminAccessGroupListItem]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].MemberCount != 3 || !page.Page.HasMore || first.Header().Get("ETag") != "" {
		t.Fatal(first.Body.String())
	}
	next := do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", actingRequestAdmin)
	if next.Code != 200 || !strings.Contains(next.Body.String(), `"id":"8"`) {
		t.Fatal(next.Code, next.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, path+"?limit=2&cursor="+page.Page.NextCursor, "", actingRequestAdmin), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, path+"?limit=1&cursor="+page.Page.NextCursor, "", bearer(otherAdminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, path, "", requestOwner), TypePermissionDenied)
}

func TestAdminAccessGroupAuthenticationPolicy(t *testing.T) {
	for _, credential := range []struct {
		name          string
		user          int
		scopes        []string
		reads, writes bool
	}{
		{"unscoped administrator", 2, nil, true, true},
		{"groups reader", 2, []string{auth.ScopeAdminAccessGroupsRead}, true, false},
		{"users manager", 2, []string{auth.ScopeAdminUsers}, false, false},
		{"ordinary account", 1, nil, false, false},
	} {
		t.Run(credential.name, func(t *testing.T) {
			f := fixtureAdminAccessGroups()
			deps := requestDeps(fixtureRequests())
			deps.AdminAccessGroups = f
			deps.Auth = apimw.NewAuthMiddleware(nil, nil, fakeAPIKeys{keys: map[string]*models.APIKey{apiKeyToken: {ID: 9, UserID: credential.user, Scopes: credential.scopes}}}, fakeUsers{users: map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}})
			h := NewHandler(deps)
			for _, op := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, "/admin/access-groups", "", 200}, {http.MethodGet, "/admin/access-groups/7", "", 200},
				{http.MethodPost, "/admin/access-groups", `{"name":"Tool"}`, 201}, {http.MethodPut, "/admin/access-groups/7", `{"name":"New"}`, 200}, {http.MethodDelete, "/admin/access-groups/7", "", 204},
			} {
				got := do(t, h, op.method, Prefix+op.path, op.body, with(bearer(apiKeyToken), "If-Match", "*"))
				allowed := credential.writes
				if op.method == http.MethodGet {
					allowed = credential.reads
				}
				expected := http.StatusForbidden
				if allowed {
					expected = op.status
				}
				if got.Code != expected {
					t.Fatalf("%s %s: %d %s", op.method, op.path, got.Code, got.Body.String())
				}
			}
			if !credential.writes && f.writes != 0 {
				t.Fatal("denied writer reached service")
			}
		})
	}
}
