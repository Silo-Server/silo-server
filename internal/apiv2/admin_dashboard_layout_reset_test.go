package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDashboardLayoutReset struct {
	users []int
	err   error
}

func (f *fakeDashboardLayoutReset) ResetAdminDashboardLayout(_ context.Context, id int) error {
	f.users = append(f.users, id)
	return f.err
}
func TestAdminDashboardLayoutReset(t *testing.T) {
	f := new(fakeDashboardLayoutReset)
	deps := pilotDeps(nil, nil)
	deps.AdminDashboardLayoutResets = f
	h := NewHandler(deps)
	path := Prefix + "/admin/dashboard/layout"
	requireProblem(t, do(t, h, "DELETE", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "DELETE", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.users) != 0 {
		t.Fatal("unauthorized reset")
	}
	for range 2 {
		rec := do(t, h, "DELETE", path, "", bearer(adminToken))
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if len(f.users) != 2 || f.users[0] <= 0 || f.users[0] != f.users[1] {
		t.Fatal(f.users)
	}
	f.err = errors.New("private-store")
	rec := do(t, h, "DELETE", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private-store") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminDashboardLayoutResets = nil
	requireProblem(t, do(t, NewHandler(deps), "DELETE", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
