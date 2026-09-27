package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

type iconSessionService struct {
	*fakeSessionService
	icon string
}

func (s iconSessionService) ListProviders() []auth.LoginProviderInfo {
	return []auth.LoginProviderInfo{{ID: "provider", IconURL: s.icon, InstallationID: 3}}
}
func TestAuthProviderIconProjection(t *testing.T) {
	const icon = "/api/v2/plugin-content/plugins/3/assets/brand%20icon.svg?size=2#logo"
	for _, tc := range []struct {
		name, icon, want string
		public           bool
		err              error
		missing          bool
		lookup           bool
	}{
		{name: "public", icon: icon, want: icon, public: true, lookup: true},
		{name: "legacy projected", icon: "/api/v1/plugins/3/assets/brand%20icon.svg?size=2#logo", want: icon, public: true, lookup: true},
		{name: "legacy private", icon: "/api/v1/plugins/3/assets/brand%20icon.svg?size=2#logo", lookup: true},
		{name: "legacy other installation", icon: "/api/v1/plugins/4/assets/icon.svg"},
		{name: "private", icon: icon, lookup: true},
		{name: "descriptor error", icon: icon, err: errors.New("unavailable"), lookup: true},
		{name: "missing seam", icon: icon, missing: true},
		{name: "external", icon: "https://provider.example.test/api/v2/plugin-content/plugins/3/assets/icon.svg", want: "https://provider.example.test/api/v2/plugin-content/plugins/3/assets/icon.svg"},
		{name: "relative external", icon: "//provider.example.test/icon.svg", want: "//provider.example.test/icon.svg"},
		{name: "other installation", icon: "/api/v2/plugin-content/plugins/4/assets/icon.svg"},
		{name: "traversal", icon: "/api/v2/plugin-content/plugins/3/assets/%2e%2e/private.svg"},
		{name: "nonasset", icon: "/api/v2/plugin-content/plugins/3/admin"},
		{name: "invalid escape", icon: "/api/v2/plugin-content/plugins/3/assets/%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			service := iconSessionService{new(fakeSessionService), tc.icon}
			deps.Sessions = service
			deps.PluginContent = &contentFixture{}
			calls := 0
			if !tc.missing {
				deps.AuthProviderIconPublic = func(_ context.Context, id int, route string) (bool, error) {
					calls++
					if id != 3 || route != "/assets/brand icon.svg" {
						t.Fatalf("lookup %d %s", id, route)
					}
					return tc.public, tc.err
				}
			}
			rec := do(t, NewHandler(deps), "GET", Prefix+"/auth/providers", "", nil)
			var out AuthProviderCollectionOutput
			if err := json.Unmarshal(rec.Body.Bytes(), &out.Body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || len(out.Body.Items) != 1 || out.Body.Items[0].IconURL != tc.want {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if (calls > 0) != tc.lookup {
				t.Fatalf("lookup calls=%d", calls)
			}
			if service.ListProviders()[0].IconURL != tc.icon {
				t.Fatal("shared provider metadata changed")
			}
		})
	}
}
