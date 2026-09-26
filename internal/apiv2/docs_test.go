package apiv2

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAPIDocsAssets(t *testing.T) {
	h := NewHandler(Dependencies{})
	for _, tc := range []struct {
		path, mediaType, contains string
	}{
		{"/api/v2/docs", "text/html", "<title>Silo API documentation</title>"},
		{"/api/v2/docs/init.js", "text/javascript", "openapi.json"},
		{"/api/v2/docs/swagger-ui-bundle.js", "text/javascript", "SwaggerUIBundle"},
		{"/api/v2/docs/swagger-ui.css", "text/css", ".swagger-ui"},
		{"/api/v2/docs/swagger-ui-bundle.js.LICENSE.txt", "text/plain", "Apache License"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			// A browser's Accept header must not enter the JSON negotiation gate.
			get := do(t, h, http.MethodGet, tc.path, "", map[string]string{"Accept": tc.mediaType})
			if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), tc.contains) {
				t.Fatalf("GET: status=%d, bytes=%d", get.Code, get.Body.Len())
			}
			if get.Header().Get("Content-Type") != tc.mediaType+"; charset=utf-8" || get.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) {
				t.Fatalf("asset headers: %v", get.Header())
			}
			if get.Header().Get("Cache-Control") != "no-cache" || get.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(get.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
				t.Fatalf("cache and browser policy: %v", get.Header())
			}
			head := do(t, h, http.MethodHead, tc.path, "", nil)
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
				t.Fatalf("HEAD: status=%d, bytes=%d, headers=%v", head.Code, head.Body.Len(), head.Header())
			}
			post := do(t, h, http.MethodPost, tc.path, "", nil)
			if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("POST: status=%d, headers=%v", post.Code, post.Header())
			}
		})
	}
	for _, path := range []string{"/api/v2/docs/missing.js", "/api/v2/docs/LICENSE", "/api/v2/docs/README.md"} {
		if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("unexpected asset %s: %d", path, rec.Code)
		}
	}
}
