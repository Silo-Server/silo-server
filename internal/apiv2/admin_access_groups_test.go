package apiv2

import (
	"context"
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
	return &fakeAdminAccessGroups{g: access.Group{ID: 7, Revision: 11, Name: "Group", MaxPlaybackQuality: "original", MaxProfiles: 5, MemberCount: 3, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}}
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
func TestAdminAccessGroupAuthenticationPolicy(t *testing.T) {
	credentials := []struct {
		name          string
		user          int
		scopes        []string
		reads, writes bool
	}{
		{"unscoped administrator", 2, nil, true, true},
		{"groups reader", 2, []string{auth.ScopeAdminAccessGroupsRead}, true, false},
		{"users manager", 2, []string{auth.ScopeAdminUsers}, false, false},
		{"ordinary account", 1, nil, false, false},
	}
	f := fixtureAdminAccessGroups()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccessGroups = f
	keys := make(map[string]*models.APIKey, len(credentials))
	for i, credential := range credentials {
		keys["sa_"+strings.ReplaceAll(credential.name, " ", "_")] = &models.APIKey{ID: int64(i + 1), UserID: credential.user, Scopes: credential.scopes}
	}
	deps.Auth = apimw.NewAuthMiddleware(nil, nil, fakeAPIKeys{keys: keys}, fakeUsers{users: map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}})
	h := NewHandler(deps)
	for _, credential := range credentials {
		t.Run(credential.name, func(t *testing.T) {
			*f = *fixtureAdminAccessGroups()
			for _, op := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, "/admin/access-groups", "", 200}, {http.MethodGet, "/admin/access-groups/7", "", 200},
				{http.MethodPost, "/admin/access-groups", `{"name":"Tool"}`, 201}, {http.MethodPut, "/admin/access-groups/7", `{"name":"New"}`, 200}, {http.MethodDelete, "/admin/access-groups/7", "", 204},
			} {
				got := do(t, h, op.method, Prefix+op.path, op.body, with(bearer("sa_"+strings.ReplaceAll(credential.name, " ", "_")), "If-Match", "*"))
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
