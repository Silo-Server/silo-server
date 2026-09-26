package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanSourceDelete struct {
	calls []string
	err   error
}

func (f *fakeAutoscanSourceDelete) DeleteAdminAutoscanSource(_ context.Context, id string) error {
	f.calls = append(f.calls, id)
	return f.err
}
func TestAdminAutoscanSourceDelete(t *testing.T) {
	f := new(fakeAutoscanSourceDelete)
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanSourceDeletes = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/sources/source-a"
	requireProblem(t, do(t, h, "DELETE", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "DELETE", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.calls) != 0 {
		t.Fatal("unauthorized deletion")
	}
	rec := do(t, h, "DELETE", path, "", bearer(adminToken))
	if rec.Code != 204 || rec.Body.Len() != 0 || len(f.calls) != 1 || f.calls[0] != "source-a" {
		t.Fatal(rec.Code, rec.Body.String(), f.calls)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{autoscan.ErrNotFound, 404}, {handlers.ErrAdminAutoscanSourceDeleteUnavailable, 503}, {errors.New("private-store-source in use by source"), 500}} {
		f.err = test.err
		rec = do(t, h, "DELETE", path, "", bearer(adminToken))
		if rec.Code != test.status || strings.Contains(rec.Body.String(), "private-store") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	deps.AdminAutoscanSourceDeletes = nil
	requireProblem(t, do(t, NewHandler(deps), "DELETE", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
