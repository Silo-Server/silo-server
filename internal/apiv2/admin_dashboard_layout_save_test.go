package apiv2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeDashboardLayoutSave struct {
	users   []int
	docs    []json.RawMessage
	err     error
	current handlers.AdminDashboardLayoutView
}

func (f *fakeDashboardLayoutSave) SaveAdminDashboardLayout(_ context.Context, id int, doc json.RawMessage, guard func(handlers.AdminDashboardLayoutView) error) (handlers.AdminDashboardLayoutView, error) {
	if err := guard(f.current); err != nil {
		return handlers.AdminDashboardLayoutView{}, err
	}
	f.users = append(f.users, id)
	f.docs = append(f.docs, append(json.RawMessage(nil), doc...))
	f.current = handlers.AdminDashboardLayoutView{Revision: "committed", Layout: doc, UpdatedAt: new(time.Unix(1, 0))}
	return f.current, f.err
}

func (f *fakeDashboardLayoutSave) ReadAdminDashboardLayout(context.Context, int) (handlers.AdminDashboardLayoutView, error) {
	return f.current, nil
}
func TestDashboardSaveRevisionPreconditions(t *testing.T) {
	f := new(fakeDashboardLayoutSave)
	deps := pilotDeps(nil, nil)
	deps.AdminDashboardLayoutSaves = f
	deps.AdminDashboardLayout = f
	h := NewHandler(deps)
	path := Prefix + "/admin/dashboard/layout"
	body := `{"layout":{"value":"B"}}`
	read := do(t, h, "GET", path, "", bearer(adminToken))
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	for _, tc := range []struct {
		name, match, none string
		status            int
	}{
		{"missing", "", "", 428}, {"weak", "W/" + tag, "", 412}, {"stale", `"stale"`, "", 412}, {"match-before-none", `"stale"`, `bad`, 412}, {"none-match", tag, tag, 412},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := bearer(adminToken)
			if tc.match != "" {
				headers["If-Match"] = tc.match
			}
			if tc.none != "" {
				headers["If-None-Match"] = tc.none
			}
			rec := do(t, h, "PUT", path, body, headers)
			if rec.Code != tc.status || len(f.docs) != 0 {
				t.Fatal(rec.Code, rec.Body.String(), f.docs)
			}
		})
	}
	headers := bearer(adminToken)
	headers["If-Match"] = tag
	rec := do(t, h, "PUT", path, body, headers)
	if rec.Code != 204 || rec.Header().Get("ETag") == "" || rec.Header().Get("ETag") == tag {
		t.Fatal(rec.Code, rec.Body.String(), rec.Header())
	}
	stale := do(t, h, "PUT", path, body, headers)
	if stale.Code != 412 || len(f.docs) != 1 {
		t.Fatal(stale.Code, f.docs)
	}
}
