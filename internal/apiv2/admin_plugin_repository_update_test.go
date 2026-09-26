package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeRepositoryUpdate struct {
	ids     []int
	inputs  []plugins.UpdateRepositoryInput
	err     error
	readErr error
	reads   int
}

func (f *fakeRepositoryUpdate) Update(_ context.Context, id int, in plugins.UpdateRepositoryInput) error {
	f.ids = append(f.ids, id)
	f.inputs = append(f.inputs, in)
	return f.err
}
func (f *fakeRepositoryUpdate) GetByID(_ context.Context, id int) (*plugins.Repository, error) {
	f.reads++
	return &plugins.Repository{ID: id, URL: "https://example.invalid", DisplayName: "Current", Enabled: false, SourceKind: "external", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, f.readErr
}
func TestAdminPluginRepositoryUpdate(t *testing.T) {
	f := new(fakeRepositoryUpdate)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginRepositoryUpdates = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/repositories/7"
	requireProblem(t, do(t, h, "PUT", path, `{}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "PUT", path, `{}`, bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "PUT", Prefix+"/admin/plugins/repositories/0", `{}`, bearer(adminToken)), TypeValidationFailed)
	if len(f.inputs) != 0 {
		t.Fatal("invalid request dispatched")
	}
	rec := do(t, h, "PUT", path, `{"url":" ","display_name":" ","enabled":false}`, bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"7"`) || len(f.inputs) != 1 || f.ids[0] != 7 || f.inputs[0].URL != nil || f.inputs[0].DisplayName != nil || f.inputs[0].Enabled == nil || *f.inputs[0].Enabled || f.reads != 1 {
		t.Fatal(rec.Code, rec.Body.String(), f.inputs)
	}
	rec = do(t, h, "PUT", path, `{"url":" https://example.invalid/new ","display_name":" New "}`, bearer(adminToken))
	if rec.Code != 200 || *f.inputs[1].URL != " https://example.invalid/new " || *f.inputs[1].DisplayName != " New " || f.inputs[1].Enabled != nil {
		t.Fatal(rec.Code, f.inputs)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{plugins.ErrRepositoryNotFound, 404}, {plugins.ErrManagedRepositoryReadOnly, 409}, {errors.New("private-store"), 500}} {
		f.err = test.err
		before := f.reads
		rec = do(t, h, "PUT", path, `{}`, bearer(adminToken))
		if rec.Code != test.status || f.reads != before || strings.Contains(rec.Body.String(), "private-store") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	f.err = nil
	f.readErr = plugins.ErrRepositoryNotFound
	rec = do(t, h, "PUT", path, `{"enabled":true}`, bearer(adminToken))
	if rec.Code != 500 {
		t.Fatal("readback disappearance must remain uncertain", rec.Code)
	}
	deps.AdminPluginRepositoryUpdates = nil
	requireProblem(t, do(t, NewHandler(deps), "PUT", path, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
}
