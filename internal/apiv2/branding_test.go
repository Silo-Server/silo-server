package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/branding"
)

type brandingSettings map[string]string

func (s brandingSettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }
func (s brandingSettings) Set(_ context.Context, key, value string) error    { s[key] = value; return nil }

type brandingAssets struct{}

func (brandingAssets) Put(context.Context, string, []byte) error { return nil }
func (brandingAssets) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	return io.NopCloser(bytes.NewReader([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))), blobstore.ObjectInfo{Key: key, Size: 42}, nil
}
func (brandingAssets) Stat(context.Context, string) (blobstore.ObjectInfo, error) {
	return blobstore.ObjectInfo{}, nil
}
func brandingHandler() http.Handler {
	settings := brandingSettings{branding.KeyServerName: "Synthetic Server", "branding.favicon_ref": "abc.svg", "ui.admin_theme_vars": `{"--primary":"red"}`, "ui.admin_custom_css": "body { color: red; }"}
	return NewHandler(Dependencies{Branding: branding.NewService(settings, brandingAssets{}), ThemeOverrides: handlers.NewThemeHandler(settings)})
}
func TestBrandingPublicDiscovery(t *testing.T) {
	h := brandingHandler()
	got := do(t, h, "GET", Prefix+"/theme/branding", "", nil)
	var body BrandingConfiguration
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got.Code != 200 || body.ServerName != "Synthetic Server" || body.FaviconURL != Prefix+"/branding/assets/favicon?v=abc.svg" || !body.StorageAvailable {
		t.Fatal(got.Code, got.Body.String())
	}
	css := do(t, h, "GET", Prefix+"/theme/admin-css", "", nil)
	if css.Code != 200 || !strings.Contains(css.Body.String(), "body { color: red; }") {
		t.Fatal(css.Code, css.Body.String())
	}
	fallback := do(t, NewHandler(Dependencies{}), "GET", Prefix+"/theme/branding", "", nil)
	if err := json.Unmarshal(fallback.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if fallback.Code != 200 || body.ServerName != branding.DefaultServerName || body.StorageAvailable {
		t.Fatal(fallback.Code, fallback.Body.String())
	}
}
func TestBrandingAssetConditionalVersionAndHead(t *testing.T) {
	h := brandingHandler()
	path := Prefix + "/branding/assets/favicon?v=abc.svg"
	got := do(t, h, "GET", path, "", map[string]string{"Accept": "image/svg+xml"})
	if got.Code != 200 || got.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(got.Body.String(), "<svg") || got.Header().Get("ETag") != `"abc.svg"` || !strings.Contains(got.Header().Get("Cache-Control"), "immutable") || got.Header().Get("Content-Security-Policy") != branding.AssetContentSecurityPolicy || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(got.Code, got.Header(), got.Body.String())
	}
	head := do(t, h, "HEAD", path, "", nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != got.Header().Get("Content-Length") {
		t.Fatal(head.Code, head.Header(), head.Body.String())
	}
	cached := do(t, h, "GET", path, "", map[string]string{"If-None-Match": `W/"abc.svg", "other"`})
	if cached.Code != 304 || cached.Body.Len() != 0 || cached.Header().Get("ETag") != `"abc.svg"` {
		t.Fatal(cached.Code, cached.Header())
	}
	requireProblem(t, do(t, h, "GET", path, "", map[string]string{"If-Match": `"stale"`}), TypePreconditionFailed)
	stale := do(t, h, "GET", Prefix+"/branding/assets/favicon?v=old.svg", "", nil)
	requireProblem(t, stale, TypeNotFound)
	if strings.Contains(stale.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("stale URL was cacheable")
	}
	current := do(t, h, "GET", Prefix+"/branding/assets/favicon", "", nil)
	if current.Code != 200 || strings.Contains(current.Header().Get("Cache-Control"), "immutable") {
		t.Fatal(current.Code, current.Header())
	}
	requireProblem(t, do(t, h, "GET", Prefix+"/branding/assets/unknown", "", nil), TypeNotFound)
	requireProblem(t, do(t, h, "GET", Prefix+"/branding/assets/mark", "", nil), TypeNotFound)
	requireProblem(t, do(t, NewHandler(Dependencies{}), "GET", path, "", nil), TypeDependencyUnavailable)
}

func TestBrandingWithoutAssetStore(t *testing.T) {
	h := NewHandler(Dependencies{Branding: branding.NewService(brandingSettings{branding.KeyServerName: "Configured"}, nil)})
	for _, path := range []string{"/theme/branding", "/theme/capabilities"} {
		got := do(t, h, "GET", Prefix+path, "", nil)
		var body map[string]any
		if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if got.Code != http.StatusOK || body["storage_available"] != false {
			t.Fatal(got.Code, got.Body.String())
		}
	}
}

func TestBrandingUploadWithoutAssetStore(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.AdminBrandingAssets = handlers.NewBrandingHandler(branding.NewService(brandingSettings{}, nil))
	body, ct := posterForm(t, "file", "image/png", 64)
	got := do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/admin/branding/assets/wordmark", body, with(bearer(adminToken), "Content-Type", ct))
	requireProblem(t, got, TypeDependencyUnavailable)
	if got.Code != http.StatusServiceUnavailable {
		t.Fatal(got.Code, got.Body.String())
	}
}
