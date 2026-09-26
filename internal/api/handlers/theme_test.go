package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestThemeSharedCatalogPreservesBridgeCacheOutcomes(t *testing.T) {
	settings := &themeSettingsStub{values: map[string]string{"theme.catalog_url": "https://raw.githubusercontent.com/example/catalog.json"}}
	h := NewThemeHandler(settings)
	calls := 0
	status := 200
	payload := ` {"version":1,"themes":[]} `
	h.httpClient.Transport = themeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	request := func(refresh bool) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/theme/catalog", nil)
		if refresh {
			h.HandleCatalogRefresh(w, r)
		} else {
			h.HandleCatalog(w, r)
		}
		return w
	}
	fresh := request(false)
	if fresh.Code != 200 || fresh.Body.String() != payload || fresh.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Fatal(fresh.Code, fresh.Header(), fresh.Body.String())
	}
	cached := request(false)
	if calls != 1 || cached.Body.String() != payload || cached.Header().Get("Cache-Control") != "" || cached.Header().Get("X-Theme-Catalog-Stale") != "" {
		t.Fatal(calls, cached.Header(), cached.Body.String())
	}
	h.catalogFetched = time.Now().Add(-2 * catalogCacheTTL)
	status = 500
	stale := request(false)
	if stale.Code != 200 || stale.Body.String() != payload || stale.Header().Get("X-Theme-Catalog-Stale") != "true" || stale.Header().Get("Cache-Control") != "" {
		t.Fatal(stale.Code, stale.Header(), stale.Body.String())
	}
	status = 200
	payload = "broken"
	invalid := request(false)
	if invalid.Code != 502 || !strings.Contains(invalid.Body.String(), `"error":"catalog_invalid"`) {
		t.Fatal(invalid.Code, invalid.Body.String())
	}
	status = 500
	refreshed := request(true)
	if refreshed.Code != 503 || !strings.Contains(refreshed.Body.String(), `"error":"catalog_unavailable"`) || refreshed.Header().Get("X-Theme-Catalog-Stale") != "" {
		t.Fatal(refreshed.Code, refreshed.Header(), refreshed.Body.String())
	}
	// A v2 caller uses the same cache that the bridge just refreshed.
	status = 200
	payload = `{"version":2,"themes":[]}`
	result, err := h.RefreshThemeCatalog(t.Context())
	if err != nil || string(result.Body) != payload || result.Stale {
		t.Fatal(result, err)
	}
	count := calls
	cached = request(false)
	if calls != count || cached.Body.String() != payload {
		t.Fatal(calls, count, cached.Body.String())
	}
}

func TestThemeSharedDownloadPreservesBridgeErrorsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, url, body, code string
		upstream, want        int
	}{
		{"missing", "", "", "bad_request", 200, 400},
		{"invalid", "://", "", "bad_request", 200, 400},
		{"forbidden", "http://raw.githubusercontent.com/theme.json", "", "host_not_allowed", 200, 403},
		{"upstream", "https://raw.githubusercontent.com/theme.json", "{}", "download_failed", 404, 502},
		{"invalid body", "https://raw.githubusercontent.com/theme.json", "bad", "download_invalid", 200, 502},
		{"portable bytes", "https://raw.githubusercontent.com/theme.json", " {\"version\":1} ", "", 200, 200},
		{"frozen cap", "https://raw.githubusercontent.com/theme.json", "{}" + strings.Repeat(" ", ThemeFileLimit-2) + "discarded", "", 200, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewThemeHandler(&themeSettingsStub{})
			calls := 0
			h.httpClient.Transport = themeRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.upstream, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			w := httptest.NewRecorder()
			h.HandleDownload(w, httptest.NewRequest("GET", "/theme/download?url="+url.QueryEscape(tc.url), nil))
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), `"error":"`+tc.code+`"`) {
				t.Fatal(w.Body.String())
			}
			if tc.want < 500 && tc.want != 200 && calls != 0 {
				t.Fatal("invalid target reached upstream")
			}
			if tc.want == 200 {
				want := tc.body[:min(len(tc.body), ThemeFileLimit)]
				if w.Body.String() != want || w.Header().Get("Content-Type") != "application/json" {
					t.Fatal("portable bytes/headers changed")
				}
			}
		})
	}
}

func TestThemeSharedFetchRejectsUnapprovedRedirect(t *testing.T) {
	for _, location := range []string{"http://raw.githubusercontent.com/redirect.json", "https://unapproved.example.test/theme.json"} {
		t.Run(location, func(t *testing.T) {
			h := NewThemeHandler(&themeSettingsStub{values: map[string]string{"theme.catalog_url": "https://raw.githubusercontent.com/example/catalog.json"}})
			calls := 0
			h.httpClient.Transport = themeRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			_, err := h.DownloadThemeFile(t.Context(), "https://raw.githubusercontent.com/example/theme.json")
			if err == nil || calls != 1 {
				t.Fatal("redirect followed", err, calls)
			}
			_, err = h.LoadThemeCatalog(t.Context())
			if err == nil || calls != 2 {
				t.Fatal("catalog redirect followed", err, calls)
			}
		})
	}
}
