package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakePluginLifecycle struct {
	calls      int
	lastID     int
	lastCreate handlers.PluginInstallationCreateInput
	lastUpdate handlers.PluginInstallationUpdateInput
	err        error
}

func (f *fakePluginLifecycle) view(id int) handlers.PluginInstallationView {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	repo := 4
	return handlers.PluginInstallationView{ID: id, RepositoryID: &repo, PluginID: "org.example.a", Version: "1.0.0", InstallPath: "/plugins/a", Enabled: true, Kind: plugins.KindPlugin, UpdatePolicy: "auto", SourceKind: "silo", CreatedAt: at, UpdatedAt: at}
}
func (f *fakePluginLifecycle) CreateAdminPluginInstallation(_ context.Context, in handlers.PluginInstallationCreateInput) (handlers.PluginInstallationView, error) {
	f.calls++
	f.lastCreate = in
	if f.err != nil {
		return handlers.PluginInstallationView{}, f.err
	}
	return f.view(21), nil
}
func (f *fakePluginLifecycle) UpdateAdminPluginInstallation(_ context.Context, id int, in handlers.PluginInstallationUpdateInput) (handlers.PluginInstallationView, error) {
	f.calls++
	f.lastID, f.lastUpdate = id, in
	if f.err != nil {
		return handlers.PluginInstallationView{}, f.err
	}
	v := f.view(id)
	if in.Enabled != nil {
		v.Enabled = *in.Enabled
	}
	if in.UpdatePolicy != nil {
		v.UpdatePolicy = *in.UpdatePolicy
	}
	return v, nil
}
func (f *fakePluginLifecycle) ApplyAdminPluginUpdate(_ context.Context, id int) (handlers.PluginInstallationView, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return handlers.PluginInstallationView{}, f.err
	}
	v := f.view(id)
	v.Version = "1.1.0"
	return v, nil
}
func (f *fakePluginLifecycle) RestartAdminPluginInstallation(_ context.Context, id int) (handlers.PluginInstallationView, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return handlers.PluginInstallationView{}, f.err
	}
	v := f.view(id)
	at := time.Date(2026, 9, 7, 0, 0, 1, 0, time.UTC)
	v.Runtime = &handlers.PluginRuntimeView{Resident: true, State: "running", RestartCount: 2, LastStartedAt: &at}
	return v, nil
}
func (f *fakePluginLifecycle) DeleteAdminPluginInstallation(_ context.Context, id int) error {
	f.calls++
	f.lastID = id
	return f.err
}

func TestAdminPluginLifecycleCreate(t *testing.T) {
	f := new(fakePluginLifecycle)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginLifecycle = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/plugins/installations"
	requireProblem(t, do(t, h, http.MethodPost, path, `{"archive_url":"https://example.invalid/a.zip"}`, bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized create reached the service")
	}
	rec := do(t, h, http.MethodPost, path, `{"repository_id":"4","plugin_id":"org.example.a","version":"1.0.0"}`, bearer(adminToken))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":"21"`) || f.lastCreate.RepositoryID == nil || *f.lastCreate.RepositoryID != 4 || f.lastCreate.PluginID != "org.example.a" {
		t.Fatal(rec.Code, rec.Body.String(), f.lastCreate)
	}
	rec = do(t, h, http.MethodPost, path, `{"archive_url":"https://example.invalid/a.zip"}`, bearer(adminToken))
	if rec.Code != http.StatusCreated || f.lastCreate.ArchiveURL != "https://example.invalid/a.zip" || f.lastCreate.RepositoryID != nil {
		t.Fatal(rec.Code, rec.Body.String(), f.lastCreate)
	}
	// Neither shape: refused before the service.
	p := requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(adminToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.archive_url" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"repository_id":"0","plugin_id":"x","version":"1"}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"archive_url":null}`, bearer(adminToken)), TypeValidationFailed)
	if f.calls != 2 {
		t.Fatalf("service saw %d creates, want 2", f.calls)
	}
	// Seam shape refusals (mixed fields, missing catalog field) are field validation.
	f.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "archive_url cannot be combined with repository install fields", Field: "archive_url"}
	p = requireProblem(t, do(t, h, http.MethodPost, path, `{"repository_id":"4","plugin_id":"a","version":"1","archive_url":"https://example.invalid/a.zip"}`, bearer(adminToken)), TypeValidationFailed)
	if p.Errors[0].Location != "body.archive_url" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Network or installer failures are internal without detail.
	f.err = errors.New("download archive \"https://example.invalid/a.zip\": connection refused")
	requireProblem(t, do(t, h, http.MethodPost, path, `{"archive_url":"https://example.invalid/a.zip"}`, bearer(adminToken)), TypeInternalError)
	f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Plugin service not configured"}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"archive_url":"https://example.invalid/a.zip"}`, bearer(adminToken)), TypeDependencyUnavailable)
	deps.AdminPluginLifecycle = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, `{"archive_url":"https://example.invalid/a.zip"}`, bearer(adminToken)), TypeDependencyUnavailable)
}

func TestAdminPluginLifecycleUpdateApplyDelete(t *testing.T) {
	f := new(fakePluginLifecycle)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginLifecycle = f
	h := newTestHandler(t, deps)
	base := Prefix + "/admin/plugins/installations/7"
	for _, tc := range []struct{ method, path, body string }{{http.MethodPut, base, `{"enabled":false}`}, {http.MethodPost, base + "/update", ""}, {http.MethodDelete, base, ""}} {
		requireProblem(t, do(t, h, tc.method, tc.path, tc.body, bearer(memberToken)), TypePermissionDenied)
	}
	if f.calls != 0 {
		t.Fatal("unauthorized mutation reached the service")
	}
	rec := do(t, h, http.MethodPut, base, `{"enabled":false,"update_policy":"notify"}`, bearer(adminToken))
	if rec.Code != http.StatusOK || f.lastID != 7 || f.lastUpdate.Enabled == nil || *f.lastUpdate.Enabled || f.lastUpdate.UpdatePolicy == nil || *f.lastUpdate.UpdatePolicy != "notify" || !strings.Contains(rec.Body.String(), `"update_policy":"notify"`) {
		t.Fatal(rec.Code, rec.Body.String(), f.lastUpdate)
	}
	requireProblem(t, do(t, h, http.MethodPut, base, `{}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, base, `{"update_policy":"sometimes"}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, base, `{"enabled":null}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, Prefix+"/admin/plugins/installations/0", `{"enabled":true}`, bearer(adminToken)), TypeValidationFailed)
	rec = do(t, h, http.MethodPost, base+"/update", "", bearer(adminToken))
	if rec.Code != http.StatusOK || f.lastID != 7 || !strings.Contains(rec.Body.String(), `"version":"1.1.0"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, base+"/restart", "", bearer(memberToken)), TypePermissionDenied)
	rec = do(t, h, http.MethodPost, base+"/restart", "", bearer(adminToken))
	if rec.Code != http.StatusOK || f.lastID != 7 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"runtime":{"resident":true,"state":"running","restart_count":2,"last_started_at":"2026-09-07T00:00:01.000Z"}`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("restart body missing %s: %s", want, rec.Body.String())
		}
	}
	rec = do(t, h, http.MethodDelete, base, "", bearer(adminToken))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || f.calls != 4 {
		t.Fatal(rec.Code, rec.Body.String(), f.calls)
	}
	// Seam errors map to one problem each on every mutation.
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{{plugins.ErrInstallationNotFound, TypeNotFound}, {handlers.ErrPluginBuiltinInstallation, TypeConflict}, {handlers.ErrPluginUpdateUnavailable, TypeConflict}, {plugins.ErrInstallationDisabled, TypeConflict}, {errors.New("stop plugin: boom"), TypeInternalError}} {
		f.err = tc.err
		requireProblem(t, do(t, h, http.MethodPut, base, `{"enabled":true}`, bearer(adminToken)), tc.want)
		requireProblem(t, do(t, h, http.MethodPost, base+"/update", "", bearer(adminToken)), tc.want)
		requireProblem(t, do(t, h, http.MethodPost, base+"/restart", "", bearer(adminToken)), tc.want)
		requireProblem(t, do(t, h, http.MethodDelete, base, "", bearer(adminToken)), tc.want)
	}
	deps.AdminPluginLifecycle = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodDelete, base, "", bearer(adminToken)), TypeDependencyUnavailable)
}
