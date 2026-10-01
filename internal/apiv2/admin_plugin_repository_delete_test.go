package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeRepositoryDelete struct {
	ids []int
	err error
}

func (f *fakeRepositoryDelete) Delete(_ context.Context, id int) error {
	f.ids = append(f.ids, id)
	return f.err
}
func TestAdminPluginRepositoryDelete(t *testing.T) {
	f := new(fakeRepositoryDelete)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginRepositoryDeletes = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/repositories/7"
	requireProblem(t, do(t, h, "DELETE", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "DELETE", path, "", bearer(memberToken)), TypePermissionDenied)
	for _, id := range []string{"0", "-1", "01", "9999999999999999999"} {
		requireProblem(t, do(t, h, "DELETE", Prefix+"/admin/plugins/repositories/"+id, "", bearer(adminToken)), TypeValidationFailed)
	}
	if len(f.ids) != 0 {
		t.Fatal("invalid deletion dispatched")
	}
	rec := do(t, h, "DELETE", path, "", bearer(adminToken))
	if rec.Code != 204 || rec.Body.Len() != 0 || len(f.ids) != 1 || f.ids[0] != 7 {
		t.Fatal(rec.Code, rec.Body.String(), f.ids)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{plugins.ErrRepositoryNotFound, 404}, {plugins.ErrManagedRepositoryReadOnly, 409}, {errors.New("private-store"), 500}} {
		f.err = test.err
		rec = do(t, h, "DELETE", path, "", bearer(adminToken))
		if rec.Code != test.status || strings.Contains(rec.Body.String(), "private-store") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	deps.AdminPluginRepositoryDeletes = nil
	requireProblem(t, do(t, NewHandler(deps), "DELETE", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
