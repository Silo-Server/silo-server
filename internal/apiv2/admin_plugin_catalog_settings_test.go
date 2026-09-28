package apiv2

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"net/http"
	"testing"
)

type fakePluginCatalogSettings struct {
	include     bool
	writes      int
	conflict    bool
	beforeWrite func()
}

func (f *fakePluginCatalogSettings) GetCatalogSettings(context.Context) (plugins.CatalogSettings, error) {
	return plugins.CatalogSettings{IncludeApprovedCommunityPlugins: f.include, ApprovedCommunityPluginCount: 2}, nil
}
func (f *fakePluginCatalogSettings) SetIncludeApprovedCommunityConditional(_ context.Context, include bool, expected *bool) (plugins.CatalogSettings, error) {
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	if f.conflict {
		return plugins.CatalogSettings{}, &plugins.CatalogSettingsConflict{Actual: false}
	}
	if expected != nil && *expected != f.include {
		return plugins.CatalogSettings{}, &plugins.CatalogSettingsConflict{Actual: f.include}
	}
	f.include = include
	f.writes++
	return plugins.CatalogSettings{IncludeApprovedCommunityPlugins: include}, nil
}
func TestAdminPluginCatalogSettingsGuard(t *testing.T) {
	f := &fakePluginCatalogSettings{}
	deps := requestDeps(fixtureRequests())
	deps.AdminPluginCatalogSettings = f
	h := NewHandler(deps)
	path := Prefix + "/admin/plugins/catalog-settings"
	read := do(t, h, "GET", path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" {
		t.Fatal(read.Code, read.Body.String())
	}
	cached := do(t, h, "GET", path, "", with(actingRequestAdmin, "If-None-Match", tag))
	if cached.Code != 304 || cached.Body.Len() != 0 {
		t.Fatal(cached.Code, cached.Body.String())
	}
	requireProblem(t, do(t, h, "PUT", path, `{"include_approved_community_plugins":true}`, actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, "PUT", path, `{"include_approved_community_plugins":null}`, with(actingRequestAdmin, "If-Match", tag)), TypeValidationFailed)
	requireProblem(t, do(t, h, "PUT", path, `{}`, with(actingRequestAdmin, "If-Match", tag)), TypeValidationFailed)
	saved := do(t, h, "PUT", path, `{"include_approved_community_plugins":true}`, with(actingRequestAdmin, "If-Match", tag))
	if saved.Code != http.StatusOK || saved.Header().Get("ETag") == tag || f.writes != 1 {
		t.Fatal(saved.Code, saved.Body.String(), f.writes)
	}
	requireProblem(t, do(t, h, "PUT", path, `{"include_approved_community_plugins":false}`, with(actingRequestAdmin, "If-Match", tag)), TypePreconditionFailed)
	f.conflict = true
	raced := do(t, h, "PUT", path, `{"include_approved_community_plugins":false}`, with(actingRequestAdmin, "If-Match", saved.Header().Get("ETag")))
	requireProblem(t, raced, TypePreconditionFailed)
	if raced.Header().Get("ETag") != tag {
		t.Fatal("race response lost current validator", raced.Header())
	}
	if f.writes != 1 {
		t.Fatal(f.writes)
	}
}

func TestAdminPluginCatalogWildcardPreservesExclusionDuringRace(t *testing.T) {
	for _, restrict := range []bool{true, false} {
		t.Run(map[bool]string{true: "excluded_concurrent_value", false: "unrestricted_wildcard"}[restrict], func(t *testing.T) {
			f := &fakePluginCatalogSettings{}
			deps := requestDeps(fixtureRequests())
			deps.AdminPluginCatalogSettings = f
			h := NewHandler(deps)
			path := Prefix + "/admin/plugins/catalog-settings"
			falseTag := do(t, h, "GET", path, "", actingRequestAdmin).Header().Get("ETag")
			f.include = true
			// The preflight observes true; a bridge writer commits false before the
			// conditional store checks the locked value.
			f.beforeWrite = func() { f.include = false }
			headers := with(actingRequestAdmin, "If-Match", "*")
			if restrict {
				headers = with(headers, "If-None-Match", falseTag)
			}
			response := do(t, h, "PUT", path, `{"include_approved_community_plugins":true}`, headers)
			if restrict {
				requireProblem(t, response, TypePreconditionFailed)
				if f.writes != 0 || f.include || response.Header().Get("ETag") != falseTag {
					t.Fatal(f, response.Header())
				}
			} else if response.Code != 200 || f.writes != 1 || !f.include {
				t.Fatal(response.Code, response.Body.String(), f)
			}
		})
	}
}
