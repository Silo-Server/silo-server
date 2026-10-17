package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/branding"
)

// fakeSettings is an in-memory branding.SettingsStore.
type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }
func (f fakeSettings) Set(_ context.Context, key, value string) error    { f[key] = value; return nil }

// fakeAssetStore is an in-memory branding.AssetStore.
type fakeAssetStore struct{ data map[string][]byte }

func (f *fakeAssetStore) Put(_ context.Context, key string, data []byte) error {
	f.data[key] = data
	return nil
}
func (f *fakeAssetStore) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	if d, ok := f.data[key]; ok {
		return io.NopCloser(bytes.NewReader(d)), blobstore.ObjectInfo{Key: key, Size: int64(len(d))}, nil
	}
	return nil, blobstore.ObjectInfo{}, blobstore.ErrNotFound
}
func (f *fakeAssetStore) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	if d, ok := f.data[key]; ok {
		return blobstore.ObjectInfo{Key: key, Size: int64(len(d))}, nil
	}
	return blobstore.ObjectInfo{}, blobstore.ErrNotFound
}

func withBranding(t *testing.T, settings fakeSettings) {
	t.Helper()
	prevFS, prevBranding := WebDistFS, Branding
	WebDistFS = fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<!doctype html><html lang="en" data-theme="midnight-cinema"><head><title>Silo</title>` +
				`<link rel="icon" href="/favicon.ico" sizes="any" /></head><body></body></html>`)},
		"favicon.ico": &fstest.MapFile{Data: []byte("STATIC_ICO")},
	}
	Branding = branding.NewService(settings, nil) // no S3: text branding only
	t.Cleanup(func() { WebDistFS, Branding = prevFS, prevBranding })
}

// TestFrontendShellCacheFollowsBrandingChanges guards the rendered-shell
// cache: one handler instance must re-render (and re-tag) the shell when the
// branding snapshot changes, not keep serving the first rendering forever.
func TestFrontendShellCacheFollowsBrandingChanges(t *testing.T) {
	settings := fakeSettings{branding.KeyServerName: "Acme Media"}
	withBranding(t, settings)
	handler := FrontendHandler()

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(first.Body.String(), "<title>Acme Media</title>") {
		t.Fatalf("initial title not branded: %q", first.Body.String())
	}

	if first.Header().Get("Content-Security-Policy") != frontendContentSecurityPolicy {
		t.Fatal("CSP missing on branded index.html")
	}

	// Repeat request with unchanged branding: same ETag (served from cache).
	repeat := httptest.NewRecorder()
	handler.ServeHTTP(repeat, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Header().Get("ETag") != repeat.Header().Get("ETag") {
		t.Fatalf("etag changed without a branding change: %q vs %q",
			first.Header().Get("ETag"), repeat.Header().Get("ETag"))
	}

	settings[branding.KeyServerName] = "Renamed Media"
	renamed := httptest.NewRecorder()
	handler.ServeHTTP(renamed, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(renamed.Body.String(), "<title>Renamed Media</title>") {
		t.Fatalf("renamed title not rendered: %q", renamed.Body.String())
	}
	if renamed.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatal("etag must change when the rendered shell changes")
	}
}

// TestFrontendShellIgnoresRetiredDefaultTheme covers the retired admin default
// theme: the web client has one theme, so a leftover branding.default_theme row
// must neither reach the shell nor change its ETag.
func TestFrontendShellIgnoresRetiredDefaultTheme(t *testing.T) {
	settings := fakeSettings{}
	withBranding(t, settings)
	handler := FrontendHandler()

	serve := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		return rr
	}

	unset := serve()
	settings["branding.default_theme"] = "cinema-light"
	stale := serve()
	if strings.Contains(stale.Body.String(), "data-default-theme") {
		t.Fatalf("shell carries the retired default theme: %q", stale.Body.String())
	}
	if stale.Header().Get("ETag") != unset.Header().Get("ETag") {
		t.Fatal("a retired setting must not change the shell ETag")
	}
}

func TestFrontendServesDynamicManifest(t *testing.T) {
	withBranding(t, fakeSettings{branding.KeyServerName: "Acme Media"})
	rr := httptest.NewRecorder()
	FrontendHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/site.webmanifest", nil))

	if ct := rr.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest content-type = %q", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if m["name"] != "Acme Media" {
		t.Fatalf("manifest name = %v", m["name"])
	}
}

func TestFrontendFaviconFallsThroughWhenNoCustom(t *testing.T) {
	withBranding(t, fakeSettings{}) // no custom favicon configured
	rr := httptest.NewRecorder()
	FrontendHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if rr.Body.String() != "STATIC_ICO" {
		t.Fatalf("expected bundled favicon fallthrough, got %q", rr.Body.String())
	}
}

// TestFrontendCustomSvgFaviconIsHardened guards the stored-XSS mitigation: an
// admin-uploaded SVG favicon must be served with nosniff + a sandboxing CSP so
// it cannot execute scripts when navigated to directly on the app origin.
func TestFrontendCustomSvgFaviconIsHardened(t *testing.T) {
	store := &fakeAssetStore{data: map[string][]byte{"branding/favicon/abc.svg": []byte("<svg/>")}}
	prevFS, prevBranding := WebDistFS, Branding
	WebDistFS = fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<title>Silo</title>")},
		"favicon.ico": &fstest.MapFile{Data: []byte("STATIC_ICO")},
	}
	Branding = branding.NewService(fakeSettings{"branding.favicon_ref": "abc.svg"}, store)
	t.Cleanup(func() { WebDistFS, Branding = prevFS, prevBranding })

	rr := httptest.NewRecorder()
	FrontendHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if rr.Code != http.StatusOK || rr.Body.String() != "<svg/>" {
		t.Fatalf("expected custom svg favicon, got status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != branding.AssetContentSecurityPolicy {
		t.Fatalf("favicon CSP = %q, want sandboxing policy", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("favicon nosniff = %q", got)
	}
}

// TestFrontendManifestRevalidatesAfterRebrand guards #2035: the manifest keeps
// a stable URL, so it must revalidate on every use and carry a content ETag.
// With a max-age, browsers kept showing the old name and theme_color for
// minutes after an admin rebranded the server.
func TestFrontendManifestRevalidatesAfterRebrand(t *testing.T) {
	settings := fakeSettings{branding.KeyServerName: "Acme Media"}
	withBranding(t, settings)
	handler := FrontendHandler()

	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/site.webmanifest", nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	first := get("")
	if got := first.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("manifest cache-control = %q, want no-cache", got)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("manifest response missing ETag")
	}
	if rr := get(etag); rr.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match exact: status = %d, want 304", rr.Code)
	}
	// A compressing proxy weakens the ETag; weak comparison must still match.
	if rr := get("W/" + etag); rr.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match weakened: status = %d, want 304", rr.Code)
	}

	settings[branding.KeyServerName] = "Renamed Media"
	settings[branding.KeyAccentColor] = "#ec4899"
	rebranded := get(etag)
	if rebranded.Code != http.StatusOK {
		t.Fatalf("revalidation after rebrand: status = %d, want 200", rebranded.Code)
	}
	if rebranded.Header().Get("ETag") == etag {
		t.Fatal("manifest ETag must change when the branding changes")
	}
	var m map[string]any
	if err := json.Unmarshal(rebranded.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if m["name"] != "Renamed Media" || m["theme_color"] != "#ec4899" {
		t.Fatalf("rebranded manifest name = %v, theme_color = %v", m["name"], m["theme_color"])
	}
}

// TestFrontendCustomFaviconRevalidatesAfterReplace guards #2035 for the bare
// /favicon.ico path that bookmarks and direct requests use: it must revalidate
// on every use, so a replaced or removed favicon shows on the next request.
func TestFrontendCustomFaviconRevalidatesAfterReplace(t *testing.T) {
	store := &fakeAssetStore{data: map[string][]byte{
		"branding/favicon/old.png": []byte("OLD_PNG"),
		"branding/favicon/new.png": []byte("NEW_PNG"),
	}}
	settings := fakeSettings{"branding.favicon_ref": "old.png"}
	prevFS, prevBranding := WebDistFS, Branding
	WebDistFS = fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<title>Silo</title>")},
		"favicon.ico": &fstest.MapFile{Data: []byte("STATIC_ICO")},
	}
	Branding = branding.NewService(settings, store)
	t.Cleanup(func() { WebDistFS, Branding = prevFS, prevBranding })
	handler := FrontendHandler()

	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	first := get("")
	if got := first.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("favicon cache-control = %q, want no-cache", got)
	}
	etag := first.Header().Get("ETag")
	if etag != `"old.png"` {
		t.Fatalf("favicon ETag = %q, want the content ref", etag)
	}
	if rr := get(etag); rr.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match unchanged: status = %d, want 304", rr.Code)
	}

	settings["branding.favicon_ref"] = "new.png"
	replaced := get(etag)
	if replaced.Code != http.StatusOK || replaced.Body.String() != "NEW_PNG" {
		t.Fatalf("after replace: status = %d body = %q, want 200 NEW_PNG", replaced.Code, replaced.Body.String())
	}

	// Removing the custom favicon falls through to the bundled file, which
	// already revalidates against its own content ETag.
	delete(settings, "branding.favicon_ref")
	removed := get(replaced.Header().Get("ETag"))
	if removed.Code != http.StatusOK || removed.Body.String() != "STATIC_ICO" {
		t.Fatalf("after removal: status = %d body = %q, want 200 STATIC_ICO", removed.Code, removed.Body.String())
	}
	if got := removed.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("bundled favicon cache-control = %q, want no-cache", got)
	}
}
