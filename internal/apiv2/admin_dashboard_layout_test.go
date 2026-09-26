package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeDashboardLayout struct {
	users []int
	view  handlers.AdminDashboardLayoutView
	err   error
}

func (f *fakeDashboardLayout) ReadAdminDashboardLayout(_ context.Context, user int) (handlers.AdminDashboardLayoutView, error) {
	f.users = append(f.users, user)
	return f.view, f.err
}
func TestAdminDashboardLayoutRead(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeDashboardLayout)
	deps.AdminDashboardLayout = f
	h := NewHandler(deps)
	path := Prefix + "/admin/dashboard/layout"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.users) != 0 {
		t.Fatal("unauthorized layout read")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"layout":null`) || !strings.Contains(rec.Body.String(), `"updated_at":null`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	absent := rec.Header().Get("ETag")
	if absent == "" || len(f.users) != 1 || f.users[0] <= 0 {
		t.Fatal("missing account or validator")
	}
	headers := bearer(adminToken)
	headers["If-None-Match"] = absent
	if rec := do(t, h, "GET", path, "", headers); rec.Code != 304 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	stamp := time.Date(2026, 9, 6, 1, 2, 3, 123456789, time.FixedZone("offset", 3600))
	f.view = handlers.AdminDashboardLayoutView{Layout: json.RawMessage(`{"version":1,"entries":[],"future":{"id":9007199254740993}}`), UpdatedAt: &stamp}
	rec = do(t, h, "GET", path, "", headers)
	if rec.Code != 200 || rec.Header().Get("ETag") == absent || !strings.Contains(rec.Body.String(), `9007199254740993`) || !strings.Contains(rec.Body.String(), `"updated_at":"2026-09-06T00:02:03.123Z"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	tag := rec.Header().Get("ETag")
	f.view.Layout = json.RawMessage(`{"future":{"id":9007199254740993},"entries":[],"version":1}`)
	headers["If-None-Match"] = tag
	if rec := do(t, h, "GET", path, "", headers); rec.Code != 304 {
		t.Fatal("JSON key order changed validator", rec.Code, rec.Body.String())
	}
	foreign := do(t, h, "GET", path, "", actingRequestAdmin)
	if foreign.Code != 200 || foreign.Header().Get("ETag") == tag {
		t.Fatal("authority does not scope validator", foreign.Code, foreign.Body.String())
	}
	headers = bearer(adminToken)
	headers["If-Match"] = absent
	requireProblem(t, do(t, h, "GET", path, "", headers), TypePreconditionFailed)
	f.err = errors.New("private storage information")
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private storage") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.AdminDashboardLayout = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
