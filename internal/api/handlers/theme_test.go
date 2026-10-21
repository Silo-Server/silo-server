package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
