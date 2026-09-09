package plugins

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type profileTestRouteClient struct {
	request *pluginv1.HandleHTTPRequest
}

func (c *profileTestRouteClient) Handle(
	_ context.Context, request *pluginv1.HandleHTTPRequest,
) (*pluginv1.HandleHTTPResponse, error) {
	c.request = request
	return &pluginv1.HandleHTTPResponse{StatusCode: http.StatusOK}, nil
}

type profileTestProxyService struct {
	client *profileTestRouteClient
	access string
}

func (s profileTestProxyService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	access := s.access
	if access == "" {
		access = "authenticated"
	}
	return []*pluginv1.HttpRouteDescriptor{{
		Method: http.MethodGet, Path: "/page", Access: access,
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
		client.request.Headers["X-Silo-Profile-Name"] != "Living Room" {
		t.Fatalf("forwarded request = %#v", client.request)
	}
}

func TestHTTPProxyRejectsOversizedPublicBodyBeforePlugin(t *testing.T) {
	client := &profileTestRouteClient{}
	proxy := NewHTTPProxy(
		profileTestProxyService{client: client, access: "public"}, profileTestInstallationStore{},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/1/page", strings.NewReader(strings.Repeat("x", 3<<20+1)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	proxy.ServeRoute(rec, req, 1, false, false)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if client.request != nil {
		t.Fatal("plugin was invoked for an oversized public request")
	}
}

func TestHTTPProxyAcceptsExactLimitForPublicAndAuthenticatedRoutes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		access        string
		authenticated bool
	}{
		{name: "public", access: "public"},
		{name: "authenticated", access: "authenticated", authenticated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &profileTestRouteClient{}
			proxy := NewHTTPProxy(
				profileTestProxyService{client: client, access: tc.access}, profileTestInstallationStore{},
			)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/1/page", strings.NewReader(strings.Repeat("x", maxPluginRouteBodyBytes)))
			rec := httptest.NewRecorder()
			proxy.ServeRoute(rec, req, 1, tc.authenticated, false)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if client.request == nil || len(client.request.GetBody()) != maxPluginRouteBodyBytes {
				t.Fatalf("forwarded body length = %d", len(client.request.GetBody()))
			}
		})
	}
}

func TestHTTPProxyRejectsUnreadableBodyBeforePlugin(t *testing.T) {
	client := &profileTestRouteClient{}
	proxy := NewHTTPProxy(
		profileTestProxyService{client: client, access: "public"}, profileTestInstallationStore{},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/1/page", nil)
	req.Body = &proxyFailingReadCloser{err: errors.New("read failed")}
	rec := httptest.NewRecorder()
	proxy.ServeRoute(rec, req, 1, false, false)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if client.request != nil {
		t.Fatal("plugin was invoked with a partial unreadable body")
	}
}

type proxyFailingReadCloser struct{ err error }

func (r *proxyFailingReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (*proxyFailingReadCloser) Close() error               { return nil }

var _ io.ReadCloser = (*proxyFailingReadCloser)(nil)
