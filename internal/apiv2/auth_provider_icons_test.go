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

func (s iconSessionService) DiscoverProviders(context.Context) (auth.ProviderDiscovery, error) {
	return auth.ProviderDiscovery{Providers: []auth.LoginProviderInfo{{ID: "provider", IconURL: s.icon, InstallationID: 3}}}, nil
}
func TestAuthProviderIconProjection(t *testing.T) {
	const icon = "/api/v2/plugin-content/plugins/3/assets/brand%20icon.svg?size=2#logo"
	deps := pilotDeps(nil, nil)
	service := &iconSessionService{fakeSessionService: new(fakeSessionService)}
	deps.Sessions = service
	deps.PluginContent = &contentFixture{}
	var lookup AuthProviderIconPublic
	deps.AuthProviderIconPublic = func(ctx context.Context, id int, route string) (bool, error) {
		return lookup(ctx, id, route)
	}
	h := NewHandler(deps)
	deps.AuthProviderIconPublic = nil
	withoutLookup := NewHandler(deps)
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
			service.icon = tc.icon
			calls := 0
			if !tc.missing {
				lookup = func(_ context.Context, id int, route string) (bool, error) {
					calls++
					if id != 3 || route != "/assets/brand icon.svg" {
						t.Fatalf("lookup %d %s", id, route)
					}
					return tc.public, tc.err
				}
			}
			handler := h
			if tc.missing {
				handler = withoutLookup
			}
			rec := do(t, handler, "GET", Prefix+"/auth/providers", "", nil)
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
			if discovery, _ := service.DiscoverProviders(context.Background()); discovery.Providers[0].IconURL != tc.icon {
				t.Fatal("shared provider metadata changed")
			}
		})
	}
}
