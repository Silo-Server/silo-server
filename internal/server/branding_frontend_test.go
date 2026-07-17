package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

// fakeSettings is an in-memory branding.SettingsStore.
type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }
func (f fakeSettings) Set(_ context.Context, key, value string) error    { f[key] = value; return nil }

// fakeAssetStore is an in-memory branding.AssetStore.
type fakeAssetStore struct{ data map[string][]byte }

func (f *fakeAssetStore) PutObject(_ context.Context, _, key string, data []byte) error {
	f.data[key] = data
	return nil
}
func (f *fakeAssetStore) GetObject(_ context.Context, _, key string) ([]byte, error) {
	if d, ok := f.data[key]; ok {
		return d, nil
	}
	return nil, s3client.ErrNotFound
}
func (f *fakeAssetStore) Bucket() string { return "test" }

func withBranding(t *testing.T, settings fakeSettings) {
	t.Helper()
	prevFS, prevBranding := WebDistFS, Branding
	WebDistFS = fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<!doctype html><head><title>Silo</title>` +
				`<link rel="icon" href="/favicon.ico" sizes="any" /></head><body></body>`)},
		"favicon.ico": &fstest.MapFile{Data: []byte("STATIC_ICO")},
	}
	Branding = branding.NewService(settings, nil) // no S3: text branding only
	t.Cleanup(func() { WebDistFS, Branding = prevFS, prevBranding })
}

func TestFrontendInjectsServerNameIntoTitle(t *testing.T) {
	withBranding(t, fakeSettings{branding.KeyServerName: "Acme Media"})
	rr := httptest.NewRecorder()
	FrontendHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(rr.Body.String(), "<title>Acme Media</title>") {
		t.Fatalf("title not branded: %q", rr.Body.String())
	}
	// CSP must still be applied to the templated shell.
	if rr.Header().Get("Content-Security-Policy") != frontendContentSecurityPolicy {
		t.Fatalf("CSP missing on branded index.html")
	}
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
