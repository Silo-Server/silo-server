package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		client.request.Headers["X-Silo-Profile-Name"] != "Living Room" {
		t.Fatalf("forwarded request = %#v", client.request)
	}
}
