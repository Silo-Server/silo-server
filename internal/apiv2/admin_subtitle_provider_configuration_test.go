package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeAdminProviderConfiguration struct {
	revision         int64
	reads, writes    int
	expected         *int64
	change           subtitles.ProviderConfigChange
	readErr, saveErr error
	result           handlers.AdminSubtitleProviderSaveResult
}

func (f *fakeAdminProviderConfiguration) GetAdminSubtitleProviderConfiguration(_ context.Context, name string) (handlers.AdminSubtitleProviderConfiguration, error) {
	f.reads++
	return handlers.AdminSubtitleProviderConfiguration{ProviderName: name, Revision: f.revision, Enabled: f.revision > 0, HasAPIKey: f.revision > 0}, f.readErr
}
func (f *fakeAdminProviderConfiguration) SaveAdminSubtitleProviderConfiguration(_ context.Context, _ string, change subtitles.ProviderConfigChange, expected *int64) (handlers.AdminSubtitleProviderSaveResult, error) {
	f.writes++
	f.expected = expected
	f.change = change
	return f.result, f.saveErr
}
func fixtureAdminProviderConfiguration() *fakeAdminProviderConfiguration {
	return &fakeAdminProviderConfiguration{revision: 4, result: handlers.AdminSubtitleProviderSaveResult{SavedRevision: 5, LocalApply: handlers.SubtitleProviderLocalApplied, LocalAppliedRevision: new(int64(9))}}
}
func TestAdminProviderConfigurationGuardedTransport(t *testing.T) {
	f := fixtureAdminProviderConfiguration()
	deps := requestDeps(fixtureRequests())
	deps.AdminSubtitleProviderConfiguration = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/subtitle-providers/subdl"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" || !strings.Contains(read.Body.String(), `"has_api_key":true`) || strings.Contains(read.Body.String(), `"api_key":`) {
		t.Fatalf("read: %d %s", read.Code, read.Body)
	}
	if out := do(t, h, http.MethodGet, path, "", with(actingRequestAdmin, "If-None-Match", tag)); out.Code != 304 || out.Body.Len() != 0 {
		t.Fatalf("conditional: %d %s", out.Code, out.Body)
	}
	for _, headers := range []map[string]string{actingRequestAdmin, with(actingRequestAdmin, "If-Match", `"old"`), with(actingRequestAdmin, "If-Match", "W/"+tag)} {
		out := do(t, h, http.MethodPut, path, `{"enabled":true}`, headers)
		if out.Code != 428 && out.Code != 412 {
			t.Fatalf("precondition: %d %s", out.Code, out.Body)
		}
	}
	for _, value := range []string{"*", tag, "W/" + tag} {
		requireProblem(t, do(t, h, http.MethodPut, path, `{"enabled":true}`, with(with(actingRequestAdmin, "If-Match", tag), "If-None-Match", value)), TypePreconditionFailed)
	}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":true,"api_key":null}`, `{"enabled":true,"unknown":1}`, `{"enabled":true,"password":"` + strings.Repeat("x", 8193) + `"}`} {
		requireProblem(t, do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", tag)), TypeValidationFailed)
	}
	other := do(t, h, http.MethodGet, Prefix+"/admin/subtitle-providers/subsource", "", actingRequestAdmin).Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodPut, path, `{"enabled":true}`, with(actingRequestAdmin, "If-Match", other)), TypePreconditionFailed)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		requireProblem(t, do(t, h, method, path, `{"enabled":true}`, viewerHeaders()), TypePermissionDenied)
	}
	if f.writes != 0 {
		t.Fatal("refused write reached application")
	}
	out := do(t, h, http.MethodPut, path, `{"enabled":true,"api_key":"","username":"draft","clear_credentials":true}`, with(actingRequestAdmin, "If-Match", tag))
	if out.Code != 200 || f.writes != 1 || f.expected == nil || *f.expected != 4 || !f.change.ClearCredentials || f.change.APIKey != "" || f.change.Username != "draft" || !f.change.Enabled {
		t.Fatalf("write: %d %s %+v", out.Code, out.Body, f.change)
	}
	if !strings.Contains(out.Body.String(), `"saved_revision":"5"`) || !strings.Contains(out.Body.String(), `"local_applied_revision":"9"`) || out.Header().Get("ETag") == tag {
		t.Fatalf("saved vs applied: %s", out.Body)
	}
	// The output tag names saved5, not the later local9.
	f.revision = 5
	savedTag := do(t, h, http.MethodGet, path, "", actingRequestAdmin).Header().Get("ETag")
	if out.Header().Get("ETag") != savedTag {
		t.Fatal("wrong saved validator")
	}
	f.saveErr = &subtitles.ProviderConfigRevisionConflict{CurrentRevision: 8}
	requireProblem(t, do(t, h, http.MethodPut, path, `{"enabled":true}`, with(actingRequestAdmin, "If-Match", savedTag)), TypePreconditionFailed)
	if f.expected == nil || *f.expected != 5 {
		t.Fatal("original revision was not delegated")
	}
	f.saveErr = nil
	if out := do(t, h, http.MethodPut, path, `{"enabled":false}`, with(actingRequestAdmin, "If-Match", "*")); out.Code != 200 || f.expected != nil {
		t.Fatalf("wildcard: %d %s", out.Code, out.Body)
	}
}
func TestAdminProviderConfigurationMissingAndOutcomes(t *testing.T) {
	for _, local := range []handlers.SubtitleProviderLocalApply{handlers.SubtitleProviderLocalApplied, handlers.SubtitleProviderLocalFailed, handlers.SubtitleProviderLocalNotConfigured, handlers.SubtitleProviderLocalUnsupported} {
		t.Run(string(local), func(t *testing.T) {
			f := fixtureAdminProviderConfiguration()
			f.revision = 0
			f.result.LocalApply = local
			f.result.LocalAppliedRevision = nil
			if local == handlers.SubtitleProviderLocalApplied {
				f.result.LocalAppliedRevision = new(int64(0))
			}
			deps := requestDeps(fixtureRequests())
			deps.AdminSubtitleProviderConfiguration = f
			h := newTestHandler(t, deps)
			path := Prefix + "/admin/subtitle-providers/subdl"
			read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
			tag := read.Header().Get("ETag")
			if read.Code != 200 || !strings.Contains(read.Body.String(), `"enabled":false`) {
				t.Fatalf("missing: %d %s", read.Code, read.Body)
			}
			requireProblem(t, do(t, h, http.MethodPut, path, `{"enabled":false}`, with(actingRequestAdmin, "If-Match", "*")), TypePreconditionFailed)
			if f.writes != 0 {
				t.Fatal("absent wildcard reached save")
			}
			out := do(t, h, http.MethodPut, path, `{"enabled":false}`, with(actingRequestAdmin, "If-Match", tag))
			if out.Code != 200 || f.expected == nil || *f.expected != 0 || !strings.Contains(out.Body.String(), `"local_apply":"`+string(local)+`"`) {
				t.Fatalf("creation/outcome: %d %s", out.Code, out.Body)
			}
		})
	}
}
func TestAdminProviderConfigurationUncertaintyAndDependencies(t *testing.T) {
	for _, saveErr := range []error{errors.New("PRIVATE lost successful commit"), &handlers.APIError{Status: 500, Message: "PRIVATE"}, &handlers.APIError{Status: 503, Message: "PRIVATE"}, &handlers.APIError{Status: 400, Message: "PRIVATE"}} {
		f := fixtureAdminProviderConfiguration()
		f.saveErr = saveErr
		deps := requestDeps(fixtureRequests())
		deps.AdminSubtitleProviderConfiguration = f
		h := newTestHandler(t, deps)
		path := Prefix + "/admin/subtitle-providers/subdl"
		tag := do(t, h, http.MethodGet, path, "", actingRequestAdmin).Header().Get("ETag")
		out := do(t, h, http.MethodPut, path, `{"enabled":false}`, with(actingRequestAdmin, "If-Match", tag))
		if out.Code < 400 || strings.Contains(out.Body.String(), "PRIVATE") || f.writes != 1 || f.reads != 2 || out.Header().Get("ETag") != "" {
			t.Fatalf("uncertainty: %d %s reads=%d writes=%d", out.Code, out.Body, f.reads, f.writes)
		}
	}
	deps := requestDeps(fixtureRequests())
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/subtitle-providers/subdl", "", actingRequestAdmin), TypeDependencyUnavailable)
}
func adminProviderConfigurationFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_provider_configuration", operationID: "getAdminSubtitleProviderConfiguration", method: http.MethodGet, path: Prefix + "/admin/subtitle-providers/subdl", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminSubtitleProviderConfiguration", scenario: "Redacted canonical configuration supplies the captured edit validator."},
		{name: "admin_provider_configuration_saved", operationID: "updateAdminSubtitleProviderConfiguration", method: http.MethodPut, path: Prefix + "/admin/subtitle-providers/subdl", headers: with(actingRequestAdmin, "If-Match", "*"), body: `{"enabled":false}`, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminSubtitleProviderConfigurationSaved", scenario: "Confirmed durable revision5 is separate from local application of later revision9."},
	}
}
