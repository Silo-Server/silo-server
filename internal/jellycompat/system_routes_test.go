package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Jellyfin serves Ping on GET and POST and answers with the product name as a
// JSON string; Branding/Css (and Css.css) answers 200 with empty text/css.
func TestRouterServesPingAndBrandingCSSLikeJellyfin(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg})

	for _, tc := range []struct {
		method, path, contentType, body string
	}{
		{http.MethodGet, "/System/Ping", "application/json", "\"Jellyfin Server\"\n"},
		{http.MethodPost, "/System/Ping", "application/json", "\"Jellyfin Server\"\n"},
		{http.MethodPost, "/system/ping", "application/json", "\"Jellyfin Server\"\n"},
		// The recorder keeps a HEAD body; net/http's server drops it.
		{http.MethodHead, "/System/Ping", "application/json", "\"Jellyfin Server\"\n"},
		{http.MethodGet, "/Branding/Css", "text/css; charset=utf-8", ""},
		{http.MethodGet, "/Branding/Css.css", "text/css; charset=utf-8", ""},
		{http.MethodGet, "/branding/css.css", "text/css; charset=utf-8", ""},
		{http.MethodGet, "/emby/Branding/Css", "text/css; charset=utf-8", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
		})
	}
}
