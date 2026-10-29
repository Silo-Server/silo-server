package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type profileTestRouteClient struct {
	request         *pluginv1.HandleHTTPRequest
	responseHeaders map[string]string
}

func (c *profileTestRouteClient) Handle(
	_ context.Context, request *pluginv1.HandleHTTPRequest,
) (*pluginv1.HandleHTTPResponse, error) {
	c.request = request
	return &pluginv1.HandleHTTPResponse{StatusCode: http.StatusOK, Headers: c.responseHeaders}, nil
}

type profileTestProxyService struct {
	client *profileTestRouteClient
}

func (s profileTestProxyService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{{
		Method: http.MethodGet, Path: "/page", Access: "authenticated",
	}}, nil
}

func (profileTestProxyService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return "", nil
}

func (s profileTestProxyService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return s.client, nil
}

type profileTestInstallationStore struct{}

func (profileTestInstallationStore) ListEnabled(context.Context) ([]*Installation, error) {
	return nil, nil
}

func (profileTestInstallationStore) ListCapabilities(context.Context, int) ([]*Capability, error) {
	return []*Capability{{Type: "http_routes.v1", ID: "routes"}}, nil
}

type profileCapturingThemeLookup struct{ profileID string }

func (l *profileCapturingThemeLookup) LookupUITheme(_ context.Context, _ int, profileID string) (string, error) {
	l.profileID = profileID
	return "dark", nil
}

type profileCapturingIdentityLookup struct{ profileID string }

func (l *profileCapturingIdentityLookup) LookupIdentity(
	_ context.Context, _ int, profileID string,
) (UserIdentity, error) {
	l.profileID = profileID
	return UserIdentity{Username: "alice", ProfileName: "Living Room"}, nil
}

func TestHTTPProxyUsesLaunchProfileWithoutRequestHeader(t *testing.T) {
	client := &profileTestRouteClient{}
	themes := &profileCapturingThemeLookup{}
	identities := &profileCapturingIdentityLookup{}
	proxy := NewHTTPProxy(
		profileTestProxyService{client: client}, profileTestInstallationStore{},
	).WithUserThemeLookup(themes).WithUserIdentityLookup(identities)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/1/page", nil)
	req = req.WithContext(WithPluginAccessUser(req.Context(), true, false, 7, "profile-1"))
	rec := httptest.NewRecorder()
	proxy.ServeRoute(rec, req, 1, true, false)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if themes.profileID != "profile-1" || identities.profileID != "profile-1" {
		t.Fatalf("lookup profiles = theme %q identity %q", themes.profileID, identities.profileID)
	}
	if client.request == nil || client.request.Headers["X-Silo-Theme"] != "dark" ||
		client.request.Headers["X-Silo-Profile-Name"] != "Living Room" ||
		client.request.Headers["X-Silo-Profile-Id"] != "profile-1" {
		t.Fatalf("forwarded request = %#v", client.request)
	}
}

// Only the profile the access resolver verified reaches the plugin. A raw
// X-Profile-Id names a profile without its PIN proof, so it must not pick
// the profile the plugin sees.
func TestHTTPProxyIgnoresUnverifiedProfileHeader(t *testing.T) {
	client := &profileTestRouteClient{}
	themes := &profileCapturingThemeLookup{}
	identities := &profileCapturingIdentityLookup{}
	proxy := NewHTTPProxy(
		profileTestProxyService{client: client}, profileTestInstallationStore{},
	).WithUserThemeLookup(themes).WithUserIdentityLookup(identities)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/1/page", nil)
	req.Header.Set("X-Profile-Id", "locked-profile")
	req = req.WithContext(WithPluginAccessUser(req.Context(), true, false, 7, ""))
	rec := httptest.NewRecorder()
	proxy.ServeRoute(rec, req, 1, true, false)

	if rec.Code != http.StatusOK || client.request == nil {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if themes.profileID != "" || identities.profileID != "" {
		t.Fatalf("lookup profiles = theme %q identity %q, want none", themes.profileID, identities.profileID)
	}
	if id, ok := client.request.Headers["X-Silo-Profile-Id"]; ok {
		t.Fatalf("forwarded X-Silo-Profile-Id = %q, want none", id)
	}
}

// A response that renews the launch cookie must not be stored by a shared
// cache, whatever Cache-Control the plugin asked for; one without keeps it.
func TestHTTPProxyKeepsRenewedCookiesOutOfSharedCaches(t *testing.T) {
	for _, renewed := range []bool{true, false} {
		client := &profileTestRouteClient{responseHeaders: map[string]string{"Cache-Control": "public, max-age=600"}}
		proxy := NewHTTPProxy(profileTestProxyService{client: client}, profileTestInstallationStore{})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/1/page", nil)
		req = req.WithContext(WithPluginAccessUser(req.Context(), true, false, 7, "profile-1"))
		rec := httptest.NewRecorder()
		var cookie *http.Cookie
		if renewed {
			cookie = &http.Cookie{Name: "silo_plugin_access", Value: "renewed"}
		}
		proxy.ServeRoute(WithRenewedCookie(rec, cookie), req, 1, true, false)
		want := "public, max-age=600"
		if renewed {
			want = "private, no-store"
		}
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Fatalf("renewed=%v: Cache-Control = %q, want %q", renewed, got, want)
		}
	}
}

type staticPageProxyService struct{ file string }

func (staticPageProxyService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{{Method: http.MethodGet, Path: "/page", Access: "authenticated", StaticAsset: true}}, nil
}

func (s staticPageProxyService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return s.file, nil
}

func (staticPageProxyService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return nil, errors.New("a static page makes no plugin call")
}

// A static plugin page is answered by http.ServeFile before any plugin call,
// and ServeFile's error responses drop Cache-Control. A response that carries
// a renewed launch cookie stays uncacheable either way.
func TestContentHandlerKeepsRenewedCookiesOnStaticPagesOutOfSharedCaches(t *testing.T) {
	file := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(file, []byte("<p>page</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := NewHTTPProxy(staticPageProxyService{file: file}, profileTestInstallationStore{})
	handler := NewContentHandler(proxy, func(*http.Request) ContentAccess {
		return ContentAccess{
			Authenticated: true, UserID: 7, ProfileID: "profile-1",
			RenewedCookie: &http.Cookie{Name: "silo_plugin_access", Value: "renewed", Path: ContentPrefix},
		}
	}, nil)
	for _, tc := range []struct {
		name, rangeHeader string
		wantStatus        int
	}{
		{"the page", "", http.StatusOK},
		{"an unsatisfiable range", "bytes=999999-", http.StatusRequestedRangeNotSatisfiable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ContentPrefix+"/plugins/1/page", nil)
			if tc.rangeHeader != "" {
				req.Header.Set("Range", tc.rangeHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || rec.Header().Get("Set-Cookie") == "" {
				t.Fatalf("status = %d, Set-Cookie = %q", rec.Code, rec.Header().Get("Set-Cookie"))
			}
			if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q, want private, no-store", got)
			}
		})
	}
}
