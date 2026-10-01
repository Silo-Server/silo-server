package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakePluginConfiguration struct {
	calls        int
	lastID       int
	lastConfig   handlers.PluginConfigInput
	lastAuth     handlers.PluginAuthBindingInput
	lastTask     handlers.PluginTaskBindingInput
	lastTaskCap  string
	err          error
	probeSuccess bool
	probeMessage string
}

func (f *fakePluginConfiguration) SetAdminPluginConfig(_ context.Context, id int, in handlers.PluginConfigInput) error {
	f.calls++
	f.lastID, f.lastConfig = id, in
	return f.err
}
func (f *fakePluginConfiguration) TestAdminPluginConfig(_ context.Context, id int, in handlers.PluginConfigInput) (handlers.PluginConnectionCheckResult, error) {
	f.calls++
	f.lastID, f.lastConfig = id, in
	if f.err != nil {
		return handlers.PluginConnectionCheckResult{}, f.err
	}
	return handlers.PluginConnectionCheckResult{Success: f.probeSuccess, Message: f.probeMessage}, nil
}
func (f *fakePluginConfiguration) SetAdminPluginAuthBinding(_ context.Context, id int, in handlers.PluginAuthBindingInput) error {
	f.calls++
	f.lastID, f.lastAuth = id, in
	return f.err
}
func (f *fakePluginConfiguration) SetAdminPluginTaskBinding(_ context.Context, id int, capabilityID string, in handlers.PluginTaskBindingInput) error {
	f.calls++
	f.lastID, f.lastTaskCap, f.lastTask = id, capabilityID, in
	return f.err
}

func pluginConfigurationHandler(f *fakePluginConfiguration) (http.Handler, Dependencies) {
	deps := pilotDeps(nil, nil)
	deps.AdminPluginConfiguration = f
	return NewHandler(deps), deps
}

// The seam's typed errors map to one problem each on every mutation, and
// no refusal reaches the seam.
func TestAdminPluginMutationRefusals(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body string }{
		{"config", "PUT", "/config", `{"key":"account","value":{"region":"us"}}`},
		{"probe", "POST", "/config/test", `{"key":"account","value":{}}`},
		{"auth", "PUT", "/auth-binding", `{"capability_id":"oidc","enabled":true,"display_order":1,"auto_provision":false,"default_login":false}`},
		{"task", "PUT", "/task-bindings/sync", `{"enabled":true,"trigger":{"type":"startup"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePluginConfiguration{probeSuccess: true}
			h, deps := pluginConfigurationHandler(f)
			path := Prefix + "/admin/plugins/installations/7" + tc.path
			requireProblem(t, do(t, h, tc.method, path, tc.body, nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, tc.method, path, tc.body, bearer(memberToken)), TypePermissionDenied)
			requireProblem(t, do(t, h, tc.method, Prefix+"/admin/plugins/installations/0"+tc.path, tc.body, bearer(adminToken)), TypeValidationFailed)
			requireProblem(t, do(t, h, tc.method, Prefix+"/admin/plugins/installations/abc"+tc.path, tc.body, bearer(adminToken)), TypeValidationFailed)
			requireProblem(t, do(t, h, tc.method, path, `{`, bearer(adminToken)), TypeMalformedRequest)
			requireProblem(t, do(t, h, tc.method, path, `{"enabled":null,"key":null,"capability_id":null,"value":null,"trigger":null}`, bearer(adminToken)), TypeValidationFailed)
			if f.calls != 0 {
				t.Fatal("refusal reached the seam", f.calls)
			}
			f.err = plugins.ErrInstallationNotFound
			requireProblem(t, do(t, h, tc.method, path, tc.body, bearer(adminToken)), TypeNotFound)
			f.err = handlers.ErrPluginBuiltinInstallation
			requireProblem(t, do(t, h, tc.method, path, tc.body, bearer(adminToken)), TypeConflict)
			f.err = errors.New("private store detail")
			if rec := do(t, h, tc.method, path, tc.body, bearer(adminToken)); rec.Code != 500 || strings.Contains(rec.Body.String(), "private store") {
				t.Fatal(rec.Code, rec.Body.String())
			}
			f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Plugin service not configured"}
			requireProblem(t, do(t, h, tc.method, path, tc.body, bearer(adminToken)), TypeDependencyUnavailable)
			deps.AdminPluginConfiguration = nil
			requireProblem(t, do(t, NewHandler(deps), tc.method, path, tc.body, bearer(adminToken)), TypeDependencyUnavailable)
		})
	}
}

func TestAdminPluginConfigWriteAndProbe(t *testing.T) {
	f := &fakePluginConfiguration{probeSuccess: true, probeMessage: "Connection successful."}
	h, _ := pluginConfigurationHandler(f)
	base := Prefix + "/admin/plugins/installations/7/config"
	rec := do(t, h, "PUT", base, `{"key":"account","value":{"region":"us-east","api_key":""},"clear_secrets":["token"]}`, actingRequestAdmin)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || f.calls != 1 || f.lastID != 7 || f.lastConfig.Key != "account" || f.lastConfig.Value["region"] != "us-east" || f.lastConfig.Value["api_key"] != "" || len(f.lastConfig.ClearSecrets) != 1 || f.lastConfig.ClearSecrets[0] != "token" {
		t.Fatal(rec.Code, rec.Body.String(), f.lastConfig)
	}
	requireProblem(t, do(t, h, "PUT", base, `{"key":" ","value":{}}`, actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, "PUT", base, `{"value":{}}`, actingRequestAdmin), TypeValidationFailed)
	// The plugin's own schema validation is the operator's feedback and is kept.
	f.err = &plugins.ConfigValidationError{Message: "region must be one of us-east, eu-west"}
	rec = do(t, h, "PUT", base, `{"key":"account","value":{"region":"mars"}}`, actingRequestAdmin)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "region must be one of") {
		t.Fatal("validation detail lost", rec.Code, rec.Body.String())
	}
	f.err = nil
	// Probe: a completed failed check is a 200 result, nothing stored.
	f.calls = 0
	rec = do(t, h, "POST", base+"/test", `{"key":"account","value":{"region":"us-east"}}`, actingRequestAdmin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"success":true`) || !strings.Contains(rec.Body.String(), "Connection successful.") || f.calls != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.probeSuccess, f.probeMessage = false, "Connection checks are not supported for this plugin yet."
	rec = do(t, h, "POST", base+"/test", `{"key":"account","value":{}}`, actingRequestAdmin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"success":false`) || !strings.Contains(rec.Body.String(), "not supported") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "POST", base+"/test", `{"key":"","value":{}}`, actingRequestAdmin), TypeValidationFailed)
}

func TestAdminPluginBindingWrites(t *testing.T) {
	f := &fakePluginConfiguration{}
	h, _ := pluginConfigurationHandler(f)
	base := Prefix + "/admin/plugins/installations/7/"
	rec := do(t, h, "PUT", base+"auth-binding", `{"capability_id":"oidc","enabled":true,"display_order":3,"auto_provision":true,"default_login":false}`, actingRequestAdmin)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("X-Silo-Restart-Required") != "true" || f.lastID != 7 || f.lastAuth != (handlers.PluginAuthBindingInput{CapabilityID: "oidc", Enabled: true, DisplayOrder: 3, AutoProvision: true, DefaultLogin: false}) {
		t.Fatal(rec.Code, rec.Header(), f.lastAuth)
	}
	requireProblem(t, do(t, h, "PUT", base+"auth-binding", `{"capability_id":" ","enabled":true,"display_order":0,"auto_provision":false,"default_login":false}`, actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, "PUT", base+"auth-binding", `{"capability_id":"oidc","enabled":true,"display_order":-1,"auto_provision":false,"default_login":false}`, actingRequestAdmin), TypeValidationFailed)
	// Repeating the same assignment converges: the seam sees the same row again.
	before := f.lastAuth
	do(t, h, "PUT", base+"auth-binding", `{"capability_id":"oidc","enabled":true,"display_order":3,"auto_provision":true,"default_login":false}`, actingRequestAdmin)
	if f.lastAuth != before || f.calls != 2 {
		t.Fatal(f.lastAuth, f.calls)
	}
	f.calls = 0
	rec = do(t, h, "PUT", base+"task-bindings/sync", `{"enabled":false,"trigger":{"type":"cron","expression":"0 * * * *"}}`, actingRequestAdmin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"restart_required":true`) || f.lastTaskCap != "sync" || f.lastTask.Enabled || f.lastTask.Trigger["expression"] != "0 * * * *" {
		t.Fatal(rec.Code, rec.Body.String(), f.lastTask)
	}
	rec = do(t, h, "PUT", base+"task-bindings/sync", `{"enabled":true}`, actingRequestAdmin)
	if rec.Code != 200 || f.calls != 2 || !f.lastTask.Enabled || len(f.lastTask.Trigger) != 0 {
		t.Fatal(rec.Code, rec.Body.String(), f.lastTask)
	}
	if rec := do(t, h, "PUT", base+"task-bindings/%20", `{"enabled":true,"trigger":{}}`, actingRequestAdmin); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusNotFound {
		t.Fatal("blank capability must be refused", rec.Code, rec.Body.String())
	}
}
