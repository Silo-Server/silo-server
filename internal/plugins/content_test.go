package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type contentTestService struct {
	request *pluginv1.HandleHTTPRequest
	access  string
	file    string
}

func (s *contentTestService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{
		{Method: "POST", Path: "/page", Access: s.access},
		{Method: "GET", Path: "/asset.txt", Access: s.access, StaticAsset: true},
	}, nil
}
func (s *contentTestService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return s.file, nil
}
func (s *contentTestService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return s, nil
}
func (s *contentTestService) Handle(_ context.Context, r *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	s.request = r
	return &pluginv1.HandleHTTPResponse{StatusCode: 299, Body: []byte("<html>opaque</html>"), Headers: map[string]string{"Content-Type": "text/html", "Location": "/api/v1/plugins/1/absolute", "Set-Cookie": "must-not-forward"}}, nil
}

func TestContentHandlerProxyAndAccess(t *testing.T) {
	service := &contentTestService{}
	theme := &profileCapturingThemeLookup{}
	proxy := NewHTTPProxy(service, profileTestInstallationStore{}).WithUserThemeLookup(theme)
	for _, tc := range []struct {
		policy string
		access ContentAccess
		status int
	}{
		{"public", ContentAccess{}, 299},
		{"authenticated", ContentAccess{}, 401},
		{"authenticated", ContentAccess{Authenticated: true, UserID: 7, ProfileID: "launch-profile"}, 299},
		{"admin", ContentAccess{Authenticated: true}, 403},
		{"admin", ContentAccess{Authenticated: true, Admin: true}, 299},
	} {
		service.access = tc.policy
		service.request = nil
		h := NewContentHandler(proxy, func(*http.Request) ContentAccess { return tc.access }, nil)
		req := httptest.NewRequest(http.MethodPost, ContentPrefix+"/plugins/1/page?repeat=a&repeat=b", strings.NewReader("opaque body"))
		req.Header.Set("Authorization", "Bearer synthetic")
		req.Header.Set("Cookie", "synthetic=secret")
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.policy, rec.Code, rec.Body)
		}
		if tc.status != 299 {
			if service.request != nil {
				t.Fatal("unauthorized dispatch")
			}
			continue
		}
		got := service.request
		if got.Path != "/page" || got.Method != "POST" || string(got.Body) != "opaque body" || got.Query == nil || got.Query.Fields["repeat"].GetStringValue() != "a" {
			t.Fatal(got)
		}
		if got.Headers["Authorization"] != "" || got.Headers["Cookie"] != "" || got.Headers["Content-Type"] != "text/plain" {
			t.Fatal(got.Headers)
		}
		if rec.Body.String() != "<html>opaque</html>" || rec.Header().Get("Location") != "/api/v1/plugins/1/absolute" || rec.Header().Get("Set-Cookie") != "" {
			t.Fatal(rec.Header(), rec.Body)
		}
		if tc.access.UserID > 0 && theme.profileID != "launch-profile" {
			t.Fatal("launch profile lost")
		}
	}
}

func TestContentHandlerCanonicalAndAssets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "asset.txt")
	if err := os.WriteFile(file, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	service := &contentTestService{access: "authenticated", file: file}
	proxy := NewHTTPProxy(service, profileTestInstallationStore{})
	assetAccess := ContentAccess{}
	h := NewContentHandler(proxy, func(*http.Request) ContentAccess { return ContentAccess{Authenticated: true} }, func(*http.Request) ContentAccess { return assetAccess })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ContentPrefix+"/plugins/%31?x=%2F", nil))
	if rec.Code != 308 || rec.Header().Get("Location") != ContentPrefix+"/plugins/%31/?x=%2F" || service.request != nil {
		t.Fatal(rec.Code, rec.Header())
	}
	req := httptest.NewRequest(http.MethodGet, ContentPrefix+"/plugin-assets/1/asset.txt", nil)
	req.Header.Set("Range", "bytes=2-4")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("route access leaked to assets", rec.Code)
	}
	assetAccess.Authenticated = true
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Fatal(rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/plugin-assets/1", 404}, {"/plugins/bad/", 400}, {"/unknown/1", 404},
	} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ContentPrefix+tc.path, nil))
		if rec.Code != tc.status {
			t.Fatal(tc, rec.Code)
		}
	}
}
