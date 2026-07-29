package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type themeSettingsStub struct {
	values map[string]string
}

func (s *themeSettingsStub) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

type themeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f themeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestThemeAdminCSSIsNotBrowserCached(t *testing.T) {
	h := NewThemeHandler(&themeSettingsStub{values: map[string]string{
		"ui.admin_theme_vars": `{"--primary":"red"}`,
		"ui.admin_custom_css": `.shell { color: red; }`,
	}})
	rec := httptest.NewRecorder()

	h.HandleAdminCSS(rec, httptest.NewRequest(http.MethodGet, "/theme/admin-css", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestThemeRemoteURLsRequireHTTPSApprovedHost(t *testing.T) {
	h := NewThemeHandler(&themeSettingsStub{values: map[string]string{
		"theme.catalog_url": "http://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json",
	}})

	catalog := httptest.NewRecorder()
	h.HandleCatalog(catalog, httptest.NewRequest(http.MethodGet, "/theme/catalog", nil))
	if catalog.Code != http.StatusBadRequest {
		t.Fatalf("HTTP catalog status = %d, want 400; body=%s", catalog.Code, catalog.Body.String())
	}

	download := httptest.NewRecorder()
	h.HandleDownload(download, httptest.NewRequest(
		http.MethodGet,
		"/theme/download?url=http%3A%2F%2Fraw.githubusercontent.com%2Ftheme.json",
		nil,
	))
	if download.Code != http.StatusForbidden {
		t.Fatalf("HTTP download status = %d, want 403; body=%s", download.Code, download.Body.String())
	}
}

func TestThemeCatalogCacheIsScopedToConfiguredURL(t *testing.T) {
	settings := &themeSettingsStub{values: map[string]string{
		"theme.catalog_url": "https://raw.githubusercontent.com/example/themes/main/one.json",
	}}
	h := NewThemeHandler(settings)
	requests := 0
	h.httpClient = &http.Client{Transport: themeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		body := `{"catalog":"one"}`
		if strings.HasSuffix(req.URL.Path, "/two.json") {
			body = `{"catalog":"two"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	first := httptest.NewRecorder()
	h.HandleCatalog(first, httptest.NewRequest(http.MethodGet, "/theme/catalog", nil))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"one"`) {
		t.Fatalf("first response = %d %s", first.Code, first.Body.String())
	}

	// Make the old cache look fresh, then change the saved origin. URL identity
	// must still force a new fetch instead of serving the previous catalog.
	h.catalogFetched = time.Now()
	settings.values["theme.catalog_url"] = "https://raw.githubusercontent.com/example/themes/main/two.json"
	second := httptest.NewRecorder()
	h.HandleCatalog(second, httptest.NewRequest(http.MethodGet, "/theme/catalog", nil))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"two"`) {
		t.Fatalf("second response = %d %s", second.Code, second.Body.String())
	}
	if requests != 2 {
		t.Fatalf("upstream requests = %d, want 2", requests)
	}
}
