package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

func TestDiscoveryScopesEnforceProjectionAndOwnerAuthority(t *testing.T) {
	deps := pilotDeps(nil, nil)
	sessions := new(fakeAdminPlaybackSessions)
	libraries := new(fakeUserLibraries)
	deps.AdminPlaybackSessions = sessions
	deps.UserLibraries = libraries
	users := map[int]*models.User{
		1: {ID: 1, Role: "user", Enabled: true},
		2: {ID: 2, Role: "admin", Enabled: true},
	}
	keys := fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_summary":        {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminSessionsSummaryRead}},
		"sa_member_summary": {ID: 2, UserID: 1, Scopes: []string{auth.ScopeAdminSessionsSummaryRead}},
		"sa_libraries":      {ID: 3, UserID: 1, Scopes: []string{auth.ScopeLibrariesRead}},
	}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, keys, fakeUsers{users: users})
	h := NewHandler(deps)
	path := Prefix + "/admin/sessions/summary"
	rec := do(t, h, "GET", path+"?user_id=7&limit=1", "", bearer("sa_summary"))
	var summary AdminPlaybackSummaryOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &summary.Body); err != nil || rec.Code != 200 || summary.Body.Count != 2 || len(summary.Body.Items) != 1 {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	for _, field := range []string{"session_id", "profile_id", "profile_name", "client_ip", "client_user_agent", "has_playback_control", "media_file_id"} {
		if strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Fatal("summary exposed diagnostic field", field)
		}
	}
	for _, query := range []string{"?user_id=0", "?limit=0", "?limit=101", "?cursor=anything"} {
		requireProblem(t, do(t, h, "GET", path+query, "", bearer("sa_summary")), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/sessions", "", bearer("sa_summary")), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path, "", bearer("sa_member_summary")), TypePermissionDenied)
	if sessions.calls != 1 {
		t.Fatal("denied request reached session loader", sessions.calls)
	}
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{LibrariesRestricted: true, AllowedLibraryIDs: []int{12}}})
	h = NewHandler(deps)
	rec = do(t, h, "GET", Prefix+"/user/libraries", "", bearer("sa_libraries"))
	if rec.Code != 200 || libraries.userID != 1 || !libraries.scope.LibrariesRestricted || len(libraries.scope.AllowedLibraryIDs) != 1 {
		t.Fatal(rec.Code, rec.Body.String(), libraries.scope)
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/libraries", "", bearer("sa_libraries")), TypePermissionDenied)
	requireProblem(t, do(t, h, "POST", Prefix+"/playback/start", `{}`, bearer("sa_libraries")), TypePermissionDenied)
}

type fixedResourceSampler nodemetrics.Snapshot

func (s fixedResourceSampler) Snapshot() nodemetrics.Snapshot { return nodemetrics.Snapshot(s) }

// A monitoring key reads the same operations as an administrator, minus the
// deployment layout (mount paths, raw preparation failures, and node
// addresses, overrides and health-blob extras) and the power to force
// aggregate refreshes.
func TestMonitorScopeWithholdsDeploymentDetails(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminResourceSampler = fixedResourceSampler{Available: true, System: &nodemetrics.SystemStats{Cores: 4, Disks: []nodemetrics.DiskStats{{Path: "/srv/secret-media", Role: "library-0", TotalGB: 10}}}}
	deps.AdminDownloadPreparations = new(fakeAdminDownloadPreparations)
	insights := new(dashboardInsightsStub)
	deps.AdminDashboardInsights = insights
	deps.AdminNodesRead = &adminNodesReadStub{nodes: []*nodepool.Node{{
		ID: 3, Name: "gpu-01", Type: "transcode", URL: "http://10.0.0.9:8090", Healthy: true, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), DownloadArtifactDirOverride: new("/srv/secret-artifacts"),
		LastStats: json.RawMessage(`{"system":{"cores":8,"disks":[{"path":"/srv/secret-scratch","role":"scratch","total_gb":5}]},"build":{"revision":"secret-build"},"artifacts":{"dir":"/srv/secret-artifacts"}}`),
	}}}
	requests := fixtureAdminRequests()
	requests.failed = true
	deps.AdminRequests = monitoredRequestQueue{requests}
	users := map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_monitor":        {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_member_monitor": {ID: 2, UserID: 1, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_admin":          {ID: 3, UserID: 2},
	}}, fakeUsers{users: users})
	h := NewHandler(deps)

	for _, path := range []string{"/admin/system/resources", "/admin/downloads/preparations", "/admin/nodes/summary", "/admin/requests", "/admin/requests/r-1/events", "/admin/requests/counts"} {
		rec := do(t, h, "GET", Prefix+path, "", bearer("sa_monitor"))
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret") {
			t.Fatal(path, rec.Code, rec.Body.String())
		}
		requireProblem(t, do(t, h, "GET", Prefix+path, "", bearer("sa_member_monitor")), TypePermissionDenied)
	}
	rec := do(t, h, "GET", Prefix+"/admin/nodes/summary", "", bearer("sa_monitor"))
	if !strings.Contains(rec.Body.String(), `"cores":8`) || !strings.Contains(rec.Body.String(), `"role":"scratch"`) {
		t.Fatal("node summary lost its resource sample", rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/nodes", "", bearer("sa_monitor")), TypePermissionDenied)
	// Request failures stay out, and the configuration that names servers
	// and routing rules is refused outright.
	if body := do(t, h, "GET", Prefix+"/admin/requests", "", bearer("sa_monitor")).Body.String(); strings.Contains(body, "last_error") || strings.Contains(body, "radarr.lan") || strings.Contains(body, "instance_name") || strings.Contains(body, "route_name") || !strings.Contains(body, `"id":"r-1"`) {
		t.Fatal("monitor request list", body)
	}
	// The counts carry the requests switch, so the key needs no settings read.
	if body := do(t, h, "GET", Prefix+"/admin/requests/counts", "", bearer("sa_monitor")).Body.String(); !strings.Contains(body, `"requests_enabled":true`) {
		t.Fatal("monitor request counts", body)
	}
	for _, path := range []string{"/admin/request-integrations", "/admin/request-settings", "/admin/request-routes"} {
		requireProblem(t, do(t, h, "GET", Prefix+path, "", bearer("sa_monitor")), TypePermissionDenied)
	}

	// A scoped key reads cached aggregates; it cannot force the queries.
	if rec := do(t, h, "GET", Prefix+"/admin/stats/top-activity?refresh=true", "", bearer("sa_monitor")); rec.Code != 200 || insights.refresh {
		t.Fatal("monitor key refreshed the aggregate", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, "GET", Prefix+"/admin/stats/top-activity?refresh=true", "", bearer("sa_admin")); rec.Code != 200 || !insights.refresh {
		t.Fatal("administrator refresh ignored", rec.Code, rec.Body.String())
	}

	// Unscoped administrators keep the full documents.
	for path, want := range map[string]string{"/admin/system/resources": "/srv/secret-media", "/admin/downloads/preparations": "No space left", "/admin/nodes": "/srv/secret-artifacts", "/admin/requests": "radarr.lan", "/admin/requests/r-1/events": "secret-router"} {
		if rec := do(t, h, "GET", Prefix+path, "", bearer("sa_admin")); rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatal(path, rec.Code, rec.Body.String())
		}
	}
}

// File paths on the catalog listing need an unscoped caller or the
// admin:catalog:paths scope. The monitoring scope, which promises no paths,
// is refused rather than answered without them, so a mirror never mistakes
// the gap for missing files.
func TestCatalogPathsScopeGatesListingFilePaths(t *testing.T) {
	deps := pilotDeps(nil, nil)
	listing := new(fakeAdminCatalogItems)
	deps.AdminCatalogItems = listing
	users := map[int]*models.User{1: {ID: 1, Role: "user", Enabled: true}, 2: {ID: 2, Role: "admin", Enabled: true}}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_monitor":      {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_paths":        {ID: 2, UserID: 2, Scopes: []string{auth.ScopeAdminCatalogPaths}},
		"sa_both":         {ID: 3, UserID: 2, Scopes: []string{auth.ScopeAdminMonitorRead, auth.ScopeAdminCatalogPaths}},
		"sa_admin":        {ID: 4, UserID: 2},
		"sa_member_paths": {ID: 5, UserID: 1, Scopes: []string{auth.ScopeAdminCatalogPaths}},
	}}, fakeUsers{users: users})
	h := NewHandler(deps)
	const items, withPaths, path = Prefix + "/admin/catalog/items", Prefix + "/admin/catalog/items?include_file_paths=true", "/media/tv/Breaking Bad/Season 01/S01E02.mkv"

	for key, allowed := range map[string]bool{"sa_monitor": false, "sa_paths": true, "sa_both": true, "sa_admin": true} {
		rec := do(t, h, "GET", Prefix+"/admin/catalog/capabilities", "", bearer(key))
		var caps AdminCatalogCapabilities
		if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil || rec.Code != 200 || caps.ItemFilePaths != allowed {
			t.Fatal(key, rec.Code, rec.Body.String(), err)
		}
		if rec := do(t, h, "GET", items, "", bearer(key)); rec.Code != 200 || strings.Contains(rec.Body.String(), "file_path") {
			t.Fatal(key, "listing without the flag", rec.Code, rec.Body.String())
		}
		if !allowed {
			calls := listing.calls
			requireProblem(t, do(t, h, "GET", withPaths, "", bearer(key)), TypePermissionDenied)
			if listing.calls != calls {
				t.Fatal(key, "denied request reached the listing")
			}
			continue
		}
		if rec := do(t, h, "GET", withPaths, "", bearer(key)); rec.Code != 200 || !strings.Contains(rec.Body.String(), path) {
			t.Fatal(key, "listing with the flag", rec.Code, rec.Body.String())
		}
	}
	requireProblem(t, do(t, h, "GET", withPaths, "", bearer("sa_member_paths")), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/sessions", "", bearer("sa_paths")), TypePermissionDenied)
}

// monitoredRequestQueue adds the admin queue reads to the request fixture.
// Its one event carries the kind of text a failed submission leaves.
type monitoredRequestQueue struct{ *fakeAdminRequests }

func (monitoredRequestQueue) CountAdminViews(context.Context, mediarequests.Viewer) (mediarequests.AdminViewCounts, error) {
	return mediarequests.AdminViewCounts{Failed: 1}, nil
}

func (monitoredRequestQueue) ListRequestEvents(context.Context, mediarequests.Viewer, string) ([]mediarequests.RequestEvent, error) {
	return []mediarequests.RequestEvent{{ID: 1, RequestID: "r-1", EventType: "outcome_failed", Message: "http://secret-router:7878 refused the connection", CreatedAt: fixedTime()}}, nil
}
