package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminAccounts struct {
	snapshot           handlers.AdminAccountView
	create             auth.CreateAccountInput
	update             models.UpdateUserInput
	writes             int
	allowImpersonation bool
	race               bool
	err                error
}

func fixtureAdminAccounts() *fakeAdminAccounts {
	return &fakeAdminAccounts{snapshot: handlers.AdminAccountView{User: handlers.AdminUserView{ID: 7, Username: "sample", Email: "sample@example.test", Role: "user", Enabled: true, MaxProfiles: 5, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, Revision: 10, GroupRevision: 3}}
}
func (*fakeAdminAccounts) AdminAccountCapabilities() (bool, bool) { return true, true }
func (f *fakeAdminAccounts) GetAdminAccount(context.Context, int) (handlers.AdminAccountView, error) {
	return f.snapshot, f.err
}
func (f *fakeAdminAccounts) CreateAdminAccount(_ context.Context, in auth.CreateAccountInput) (int, error) {
	f.create = in
	f.writes++
	return 7, f.err
}
func (f *fakeAdminAccounts) UpdateAdminAccount(_ context.Context, _ int, rev, group int64, in models.UpdateUserInput) (int64, error) {
	if f.race || (rev != -1 && rev != f.snapshot.Revision) || group != f.snapshot.GroupRevision {
		return 0, auth.ErrAdminUserRevision
	}
	f.update = in
	f.writes++
	f.snapshot.Revision++
	return f.snapshot.Revision, nil
}
func (f *fakeAdminAccounts) DeleteAdminAccount(_ context.Context, _ int, rev, group int64) error {
	if f.race || (rev != -1 && rev != f.snapshot.Revision) || group != f.snapshot.GroupRevision {
		return auth.ErrAdminUserRevision
	}
	f.writes++
	return nil
}
func (f *fakeAdminAccounts) ImpersonateAdminAccount(context.Context, int, string, string) (handlers.TokenPairView, error) {
	if f.allowImpersonation {
		return handlers.TokenPairView{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresIn: 3600, User: handlers.UserView{ID: 7, Username: "sample", Role: "user"}}, nil
	}
	return handlers.TokenPairView{}, auth.ErrImpersonationNotAllowed
}
func (*fakeAdminAccounts) ListAdminAccountProfiles(context.Context, int) ([]handlers.AdminProfileView, error) {
	return []handlers.AdminProfileView{{ID: "profile-1", Name: "Parent"}}, nil
}

func TestAdminAccountEffectiveLibraryAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []int
		want string
	}{
		{name: "unrestricted", ids: nil, want: `null`},
		{name: "deny all", ids: []int{}, want: `[]`},
		{name: "restricted", ids: []int{3, 7}, want: `["3","7"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureAdminAccounts()
			f.snapshot.User.EffectivePolicy.LibraryIDs = tc.ids
			deps := requestDeps(fixtureRequests())
			deps.AdminAccounts = f
			deps.AdminUsers = fakeAdminUsers{users: []handlers.AdminUserView{f.snapshot.User}}
			h := NewHandler(deps)
			for _, path := range []string{Prefix + "/admin/users/7", Prefix + "/admin/users"} {
				reply := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
				if reply.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", path, reply.Code, reply.Body.String())
				}
				type row struct {
					EffectivePolicy struct {
						LibraryIDs json.RawMessage `json:"library_ids"`
					} `json:"effective_policy"`
				}
				var body struct {
					row
					Items []row `json:"items"`
				}
				if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if path == Prefix+"/admin/users" {
					if len(body.Items) != 1 {
						t.Fatalf("list returned %d accounts", len(body.Items))
					}
					body.row = body.Items[0]
				}
				if got := string(body.EffectivePolicy.LibraryIDs); got != tc.want {
					t.Errorf("%s: effective library_ids = %s, want %s", path, got, tc.want)
				}
			}
		})
	}
}

func TestAdminAccountTransportGuardAndPresence(t *testing.T) {
	f := fixtureAdminAccounts()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = f
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	cached := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", "W/"+tag))
	if cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatal(cached.Code, cached.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{"enabled":false}`, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, `{"enabled":null}`, with(actingRequestAdmin, "If-Match", tag)), TypeValidationFailed)
	saved := do(t, h, http.MethodPut, path, `{"library_ids":[],"max_streams":0,"download_allowed":false,"access_group_id":null,"permissions":[]}`, with(actingRequestAdmin, "If-Match", tag))
	if saved.Code != 204 || saved.Body.Len() != 0 {
		t.Fatal(saved.Code, saved.Body.String())
	}
	u := f.update
	if !u.LibraryIDs.Set || u.LibraryIDs.Value == nil || *u.LibraryIDs.Value == nil || !u.AccessGroupID.Set || u.AccessGroupID.Value != nil || u.DownloadAllowed.Value == nil || *u.DownloadAllowed.Value || u.MaxStreams.Value == nil || *u.MaxStreams.Value != 0 || u.Permissions == nil || *u.Permissions == nil {
		t.Fatalf("presence lost: %+v", u)
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{"password":"new-password"}`, with(actingRequestAdmin, "If-Match", tag)), TypePreconditionFailed)
	if f.writes != 1 {
		t.Fatal(f.writes)
	}
	read = do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag = read.Header().Get("ETag")
	f.snapshot.GroupRevision++
	requireProblem(t, do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag)), TypePreconditionFailed)
	read = do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag = read.Header().Get("ETag")
	f.race = true
	requireProblem(t, do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag)), TypePreconditionFailed)
	if f.writes != 1 {
		t.Fatal("stale delete ran")
	}
}
func TestAdminAccountCreateAndErrors(t *testing.T) {
	f := fixtureAdminAccounts()
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = f
	h := NewHandler(deps)
	path := Prefix + "/admin/users"
	body := `{"username":"new-user","email":"new@example.test","password":"new-password","role":"user","create_default_profile":false,"library_ids":[],"download_allowed":false}`
	reply := do(t, h, http.MethodPost, path, body, actingRequestAdmin)
	if reply.Code != 201 || reply.Header().Get("Location") != path+"/7" || !strings.Contains(reply.Body.String(), `"id":"7"`) {
		t.Fatal(reply.Code, reply.Body.String())
	}
	if f.create.DefaultProfile.Enabled || f.create.User.LibraryIDs == nil || f.create.User.DownloadAllowed == nil || *f.create.User.DownloadAllowed {
		t.Fatalf("creation lost explicit values: %+v", f.create)
	}
	f.err = auth.ErrDuplicate
	requireProblem(t, do(t, h, http.MethodPost, path, body, actingRequestAdmin), TypeConflict)
	f.err = errors.New("storage down")
	requireProblem(t, do(t, h, http.MethodGet, path+"/7", "", actingRequestAdmin), TypeInternalError)
	f.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, path+"/7", "", actingRequestAdmin), TypeNotFound)
	f.err = nil
	requireProblem(t, do(t, h, http.MethodPost, path+"/7/impersonate", "", actingRequestAdmin), TypePermissionDenied)
	profiles := do(t, h, http.MethodGet, path+"/7/profiles", "", actingRequestAdmin)
	if profiles.Code != 200 || !strings.Contains(profiles.Body.String(), `"id":"profile-1"`) {
		t.Fatal(profiles.Code, profiles.Body.String())
	}
}
