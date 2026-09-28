package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

type fakeAdminRequests struct {
	handlers.RequestService
	settings                  mediarequests.Settings
	integration               mediarequests.Integration
	limit                     mediarequests.UserLimit
	viewer                    mediarequests.Viewer
	writes                    int
	stale                     bool
	filter                    mediarequests.ListFilter
	action, reason, requestID string
}

func fixtureAdminRequests() *fakeAdminRequests {
	return &fakeAdminRequests{settings: mediarequests.Settings{RequestsEnabled: true, GlobalMaxRequests: 5, GlobalWindowDays: 7, Revision: 1}, integration: mediarequests.Integration{ID: "integration-1", Name: "Router", CapabilityID: "arr", InstallationID: new(7), BaseURL: "https://router.example.test", APIKeyRef: "private-key", UpdatedAt: fixedTime(), Revision: 2}, limit: mediarequests.UserLimit{UserID: 2, LimitMode: mediarequests.LimitModeInherit, ApprovalMode: mediarequests.ApprovalModeInherit, Revision: 3}}
}
func (f *fakeAdminRequests) GetSettings(_ context.Context, v mediarequests.Viewer) (mediarequests.Settings, error) {
	f.viewer = v
	return f.settings, nil
}
func (f *fakeAdminRequests) UpdateSettingsConditional(_ context.Context, v mediarequests.Viewer, r mediarequests.Settings, rev int64) (mediarequests.Settings, error) {
	f.viewer = v
	if f.stale {
		f.settings.Revision++
		return mediarequests.Settings{}, mediarequests.ErrStaleRevision
	}
	if rev != -1 && rev != f.settings.Revision {
		return mediarequests.Settings{}, mediarequests.ErrStaleRevision
	}
	f.writes++
	r.Revision = f.settings.Revision + 1
	f.settings = r
	return r, nil
}
func (f *fakeAdminRequests) GetUserLimit(_ context.Context, v mediarequests.Viewer, id int) (*mediarequests.UserLimit, error) {
	f.viewer = v
	if id != 2 {
		return nil, mediarequests.ErrNotFound
	}
	return &f.limit, nil
}
func (f *fakeAdminRequests) UpsertUserLimitConditional(_ context.Context, v mediarequests.Viewer, r mediarequests.UserLimit, rev int64) (*mediarequests.UserLimit, error) {
	f.viewer = v
	if rev != -1 && rev != f.limit.Revision {
		return nil, mediarequests.ErrStaleRevision
	}
	f.writes++
	r.Revision = f.limit.Revision + 1
	f.limit = r
	return &r, nil
}
func (f *fakeAdminRequests) GetIntegration(_ context.Context, v mediarequests.Viewer, id string) (*mediarequests.Integration, error) {
	f.viewer = v
	if id != f.integration.ID {
		return nil, mediarequests.ErrNotFound
	}
	return &f.integration, nil
}
func (f *fakeAdminRequests) ListIntegrations(_ context.Context, v mediarequests.Viewer) ([]mediarequests.Integration, error) {
	f.viewer = v
	return []mediarequests.Integration{f.integration}, nil
}
func (f *fakeAdminRequests) CreateIntegration(_ context.Context, v mediarequests.Viewer, r mediarequests.Integration) (*mediarequests.Integration, error) {
	f.viewer = v
	f.writes++
	r.ID = "created-1"
	r.UpdatedAt = fixedTime()
	r.Revision = 4
	return &r, nil
}
func (f *fakeAdminRequests) UpdateIntegrationConditional(_ context.Context, v mediarequests.Viewer, r mediarequests.Integration, rev int64) (*mediarequests.Integration, error) {
	f.viewer = v
	if rev != -1 && rev != f.integration.Revision {
		return nil, mediarequests.ErrStaleRevision
	}
	f.writes++
	r.Revision = f.integration.Revision + 1
	r.UpdatedAt = fixedTime()
	f.integration = r
	return &r, nil
}
func (f *fakeAdminRequests) DeleteIntegrationConditional(_ context.Context, v mediarequests.Viewer, _ string, rev int64) error {
	f.viewer = v
	if rev != -1 && rev != f.integration.Revision {
		return mediarequests.ErrStaleRevision
	}
	f.writes++
	return nil
}
func (f *fakeAdminRequests) LoadIntegrationOptions(_ context.Context, v mediarequests.Viewer, r mediarequests.Integration) (map[string][]mediarequests.RouterOption, error) {
	f.viewer = v
	if r.APIKeyRef == "bad" {
		return nil, &mediarequests.ValidationError{FieldErrors: map[string]string{"api_key_ref": "invalid key"}}
	}
	if r.APIKeyRef == "unreachable" {
		return nil, fmt.Errorf("%w: dial tcp: connect: connection refused", mediarequests.ErrIntegrationUnreachable)
	}
	return map[string][]mediarequests.RouterOption{}, nil
}

// An integration the host cannot reach answers with the upstream-unavailable
// problem instead of a generic internal error.
func TestAdminRequestOptionsUnreachableIntegration(t *testing.T) {
	h := adminRequestsHandler(fixtureAdminRequests())
	rec := do(t, h, http.MethodPost, Prefix+"/admin/request-integrations/new/options", `{"api_key_ref":"unreachable"}`, actingRequestAdmin)
	requireProblem(t, rec, TypeDependencyUnavailable)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatal("upstream failure detail leaked")
	}
}
func (f *fakeAdminRequests) ListAdmin(_ context.Context, v mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	f.viewer = v
	f.filter = filter
	rows := []*mediarequests.Request{fixtureMediaRequest("r-3", 3), fixtureMediaRequest("r-2", 2), fixtureMediaRequest("r-1", 1)}
	out := []*mediarequests.Request{}
	for _, r := range rows {
		if filter.Before != nil && r.ID >= filter.Before.ID {
			continue
		}
		out = append(out, r)
		if len(out) == filter.Limit {
			break
		}
	}
	return out, nil
}
func (f *fakeAdminRequests) moderate(v mediarequests.Viewer, action, id, reason string) (*mediarequests.Request, error) {
	f.viewer = v
	f.action, f.requestID, f.reason = action, id, reason
	f.writes++
	return fixtureMediaRequest(id, 1), nil
}
func (f *fakeAdminRequests) Approve(_ context.Context, v mediarequests.Viewer, id string) (*mediarequests.Request, error) {
	return f.moderate(v, "approve", id, "")
}
func (f *fakeAdminRequests) Decline(_ context.Context, v mediarequests.Viewer, id, reason string) (*mediarequests.Request, error) {
	return f.moderate(v, "decline", id, reason)
}
func (f *fakeAdminRequests) Cancel(_ context.Context, v mediarequests.Viewer, id, reason string) (*mediarequests.Request, error) {
	return f.moderate(v, "cancel", id, reason)
}
func (f *fakeAdminRequests) Retry(_ context.Context, v mediarequests.Viewer, id string) (*mediarequests.Request, error) {
	return f.moderate(v, "retry", id, "")
}
func adminRequestsHandler(f *fakeAdminRequests) http.Handler {
	deps := requestDeps(fixtureRequests())
	deps.AdminRequests = f
	return NewHandler(deps)
}

var actingRequestAdmin = with(bearer(adminToken), "X-Profile-Id", "p-primary")

const requestSettingsBody = `{"requests_enabled":true,"global_max_requests":6,"global_window_days":7,"global_auto_approval_enabled":false,"force_dual_quality":false}`
const requestIntegrationBody = `{"name":"Router","capability_id":"arr","installation_id":"7","supported_media_types":["movie"],"plugin_config":{},"enabled":true,"base_url":"https://router.example.test","api_key_ref":"private-key"}`

func TestAdminRequestSettingsGuards(t *testing.T) {
	f := fixtureAdminRequests()
	h := adminRequestsHandler(f)
	path := Prefix + "/admin/request-settings"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if read.Code != 200 || read.Header().Get("ETag") == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	tag := read.Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodPut, path, requestSettingsBody, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, requestSettingsBody, with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	if f.writes != 0 {
		t.Fatal("guard allowed effects")
	}
	saved := do(t, h, http.MethodPut, path, requestSettingsBody, with(actingRequestAdmin, "If-Match", tag))
	if saved.Code != 200 || saved.Header().Get("ETag") == tag {
		t.Fatal(saved.Code, saved.Body.String())
	}
	if !f.viewer.IsAdmin || f.viewer.UserID != 2 || f.viewer.ProfileID != "p-primary" {
		t.Fatalf("actor: %+v", f.viewer)
	}
	f.stale = true
	raced := do(t, h, http.MethodPut, path, requestSettingsBody, with(actingRequestAdmin, "If-Match", saved.Header().Get("ETag")))
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") == saved.Header().Get("ETag") {
		t.Fatal("race returned old tag")
	}
}
func TestAdminRequestAuthorityAndCapabilities(t *testing.T) {
	f := fixtureAdminRequests()
	h := adminRequestsHandler(f)
	for _, path := range []string{"/admin/requests", "/admin/request-settings", "/admin/request-integrations", "/admin/request-users/2/limit"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", requestOwner), TypePermissionDenied)
		requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	}
	deps := requestDeps(fixtureRequests())
	h = NewHandler(deps)
	r := do(t, h, http.MethodGet, Prefix+"/admin/requests/capabilities", "", actingRequestAdmin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"available":false`) {
		t.Fatal(r.Code, r.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests", "", actingRequestAdmin), TypeDependencyUnavailable)
}
func TestAdminRequestIntegrationSecretsAndGuard(t *testing.T) {
	f := fixtureAdminRequests()
	h := adminRequestsHandler(f)
	path := Prefix + "/admin/request-integrations/integration-1"
	r := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if r.Code != 200 || strings.Contains(r.Body.String(), "private-key") || strings.Contains(r.Body.String(), "api_key_ref") || !strings.Contains(r.Body.String(), `"has_api_key":true`) {
		t.Fatal(r.Code, r.Body.String())
	}
	tag := r.Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodDelete, path, "", actingRequestAdmin), TypePreconditionRequired)
	missing := do(t, h, http.MethodDelete, Prefix+"/admin/request-integrations/missing", "", with(actingRequestAdmin, "If-Match", `"stale"`))
	requireProblem(t, missing, TypeNotFound)
	f.integration.Revision++
	requireProblem(t, do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag)), TypePreconditionFailed)
	if f.writes != 0 {
		t.Fatal("stale delete changed state")
	}
	saved := do(t, h, http.MethodPut, path, requestIntegrationBody, with(actingRequestAdmin, "If-Match", "*"))
	if saved.Code != 200 || strings.Contains(saved.Body.String(), "private-key") {
		t.Fatal(saved.Code, saved.Body.String())
	}
	deleted := do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", saved.Header().Get("ETag")))
	if deleted.Code != 204 || deleted.Header().Get("ETag") != "" {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
	created := do(t, h, http.MethodPost, Prefix+"/admin/request-integrations", requestIntegrationBody, actingRequestAdmin)
	if created.Code != 201 || created.Header().Get("Location") != Prefix+"/admin/request-integrations/created-1" {
		t.Fatal(created.Code, created.Body.String())
	}
}
func TestAdminRequestLimitsModerationAndOptions(t *testing.T) {
	f := fixtureAdminRequests()
	h := adminRequestsHandler(f)
	path := Prefix + "/admin/request-users/2/limit"
	r := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"user_id":"2"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	body := `{"limit_mode":"custom","max_requests":3,"window_days":7,"approval_mode":"manual"}`
	saved := do(t, h, http.MethodPut, path, body, with(actingRequestAdmin, "If-Match", r.Header().Get("ETag")))
	if saved.Code != 200 {
		t.Fatal(saved.Code, saved.Body.String())
	}
	for _, tc := range []struct{ action, body, reason string }{
		{"approve", `{}`, ""},
		{"decline", `{"reason":"Already available"}`, "Already available"},
		{"cancel", `{"reason":"No longer needed"}`, "No longer needed"},
		{"retry", `{}`, ""},
	} {
		before := f.writes
		r := do(t, h, http.MethodPost, Prefix+"/admin/requests/r-1/"+tc.action, tc.body, actingRequestAdmin)
		if r.Code != 200 {
			t.Fatal(tc.action, r.Code, r.Body.String())
		}
		if f.writes != before+1 || f.action != tc.action || f.requestID != "r-1" || f.reason != tc.reason {
			t.Fatalf("%s dispatched writes=%d action=%q request=%q reason=%q", tc.action, f.writes-before, f.action, f.requestID, f.reason)
		}
	}
	p := requireProblem(t, do(t, h, http.MethodPost, Prefix+"/admin/request-integrations/new/options", `{"api_key_ref":"bad"}`, actingRequestAdmin), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.api_key_ref" {
		t.Fatalf("validation %+v", p)
	}
}
func TestAdminRequestCursorBoundaries(t *testing.T) {
	f := fixtureAdminRequests()
	h := adminRequestsHandler(f)
	r := do(t, h, http.MethodGet, Prefix+"/admin/requests?limit=1", "", actingRequestAdmin)
	var page MediaRequestCollection
	decodeBody(t, r.Body, &page)
	if r.Code != 200 || len(page.Items) != 1 || page.Page.NextCursor == "" {
		t.Fatal(r.Code, r.Body.String())
	}
	cursor := page.Page.NextCursor
	r = do(t, h, http.MethodGet, Prefix+"/admin/requests?limit=1&cursor="+cursor, "", actingRequestAdmin)
	if r.Code != 200 || f.filter.Before == nil || f.filter.Before.ID != "r-3" || !f.filter.Before.CreatedAt.Equal(fixedTime()) {
		t.Fatal(r.Code, r.Body.String(), f.filter)
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests?limit=1&outcome=failed&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests?offset=1", "", actingRequestAdmin), TypeValidationFailed)
}

func adminRequestFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "admin_request_settings_ok", operationID: "getAdminRequestSettings", path: Prefix + "/admin/request-settings", schema: "AdminRequestSettings"},
		{name: "admin_request_limit_ok", operationID: "getAdminRequestUserLimit", path: Prefix + "/admin/request-users/2/limit", schema: "AdminRequestUserLimit"},
		{name: "admin_request_integration_ok", operationID: "getRequestIntegration", path: Prefix + "/admin/request-integrations/integration-1", schema: "AdminRequestIntegration"},
		{name: "admin_request_integrations_ok", operationID: opListRequestIntegrations, path: Prefix + "/admin/request-integrations", schema: "CollectionAdminRequestIntegration"},
		{name: "admin_requests_ok", operationID: opListAdminRequests, path: Prefix + "/admin/requests", schema: "MediaRequestCollection"},
		{name: "admin_request_integration_created", operationID: "createRequestIntegration", method: http.MethodPost, path: Prefix + "/admin/request-integrations", body: requestIntegrationBody, schema: "AdminRequestIntegration", status: 201},
	}
	for i := range cases {
		c := &cases[i]
		if c.method == "" {
			c.method = http.MethodGet
		}
		if c.status == 0 {
			c.status = 200
		}
		c.headers = actingRequestAdmin
		c.scenario = "Acting administrator reads or configures requests with synthetic integration state."
		c.schema = "#/components/schemas/" + c.schema
		c.assertHeaders = []string{"Content-Type", "Cache-Control"}
		if strings.Contains(c.name, "settings") || strings.Contains(c.name, "limit") || c.name == "admin_request_integration_ok" || c.status == 201 {
			c.assertHeaders = append(c.assertHeaders, "ETag")
		}
		if c.status == 201 {
			c.assertHeaders = append(c.assertHeaders, "Location")
		}
	}
	return cases
}
