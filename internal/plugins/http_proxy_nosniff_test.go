package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type nosniffRouteClient struct{}

func (nosniffRouteClient) Handle(context.Context, *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	return &pluginv1.HandleHTTPResponse{
		StatusCode: http.StatusOK,
		Headers: map[string]string{
			"Content-Type":           "text/plain",
			"X-Content-Type-Options": "sniff-please",
			"Set-Cookie":             "stolen=1",
		},
		Body: []byte("<script>alert(1)</script>"),
	}, nil
}

type nosniffProxyService struct{ assetPath string }

func (nosniffProxyService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{
		{Method: http.MethodGet, Path: "/page", Access: "public"},
		{Method: http.MethodGet, Path: "/static/*", Access: "public", StaticAsset: true},
	}, nil
}

func (s nosniffProxyService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return s.assetPath, nil
}

func (nosniffProxyService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return nosniffRouteClient{}, nil
}

// TestHTTPProxyPluginContentIsNosniff: plugin route responses and static
// assets carry X-Content-Type-Options: nosniff, whatever the plugin sends,
// and the plugin's other headers are still filtered.
func TestHTTPProxyPluginContentIsNosniff(t *testing.T) {
	asset := filepath.Join(t.TempDir(), "app.txt")
	if err := os.WriteFile(asset, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy := NewHTTPProxy(nosniffProxyService{assetPath: asset}, profileTestInstallationStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/v2/plugins/1/page", nil)
	req = req.WithContext(WithPluginAccess(req.Context(), false, false))
	rec := httptest.NewRecorder()
	proxy.ServeRoute(rec, req, 1, false, false)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		rec.Header().Get("Content-Type") != "text/plain" || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("route: status = %d headers = %v", rec.Code, rec.Header())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v2/plugins/1/static/app.txt", nil)
	req = req.WithContext(WithPluginAccess(req.Context(), false, false))
	rec = httptest.NewRecorder()
	proxy.ServeAsset(rec, req, 1, "static/app.txt")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Body.String() != "hello" {
		t.Fatalf("asset: status = %d headers = %v body = %q", rec.Code, rec.Header(), rec.Body.String())
	}
}
