package apiv2

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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
	settings := brandingSettings{branding.KeyServerName: "Synthetic Server", "branding.favicon_ref": "abc.svg", branding.KeyAccentColor: "#5bc39d"}
	return NewHandler(Dependencies{Branding: branding.NewService(settings, brandingAssets{})})
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
