package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFrontendHandlerSetsSecurityHeadersOnSPAHTML(t *testing.T) {
	prev := WebDistFS
	WebDistFS = fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"assets/app.js": &fstest.MapFile{
			Data: []byte("console.log(1)"),
		},
	}
	t.Cleanup(func() { WebDistFS = prev })

	handler := FrontendHandler()

	for _, path := range []string{"/", "/index.html", "/library/ebooks"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			if got := rr.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("content-type = %q", got)
			}
			csp := rr.Header().Get("Content-Security-Policy")
			if csp != frontendContentSecurityPolicy {
				t.Fatalf("csp = %q", csp)
			}
			// The ebook-reader threat model depends on these directives; fail
			// loudly if they are weakened.
			for _, directive := range []string{
				"script-src 'self' 'wasm-unsafe-eval'",
				"object-src 'none'",
				"base-uri 'self'",
			} {
				if !strings.Contains(csp, directive) {
					t.Fatalf("csp missing %q: %q", directive, csp)
				}
			}
			if strings.Contains(csp, "script-src 'self' 'wasm-unsafe-eval' ") {
				t.Fatalf("script-src must not carry extra sources: %q", csp)
			}
			if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("x-content-type-options = %q", got)
			}
		})
	}
}

func TestFrontendHandlerServesStaticAssetsWithoutCSP(t *testing.T) {
	prev := WebDistFS
	WebDistFS = fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<!doctype html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
	t.Cleanup(func() { WebDistFS = prev })

	handler := FrontendHandler()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("static asset should not carry the SPA CSP, got %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("x-content-type-options = %q", got)
	}
}

func TestFrontendHandlerReturns404ForMissingAssets(t *testing.T) {
	handler := newFrontendTestHandler(t)

	// A content-hashed chunk from a previous build no longer exists after a
	// deploy. Serving the SPA shell at a .js URL makes the browser fail with
	// "Failed to fetch dynamically imported module"; a 404 lets clients (and
	// the preload-error reload handler) see the real condition.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/view-OldHash.js", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Fatalf("missing asset served as HTML (%q), the SPA fallback must not swallow /assets/", ct)
	}

	// Non-asset app routes still get the shell.
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library/ebooks", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("SPA route = %d %q, want 200 HTML", rr.Code, rr.Header().Get("Content-Type"))
	}
}

func newFrontendTestHandler(t *testing.T) http.Handler {
	t.Helper()
	prev := WebDistFS
	WebDistFS = fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		"sw.js":         &fstest.MapFile{Data: []byte("self.addEventListener('fetch', () => {})")},
	}
	t.Cleanup(func() { WebDistFS = prev })
	return FrontendHandler()
}

func TestFrontendShellCacheHeadersAndConditionalGet(t *testing.T) {
	handler := newFrontendTestHandler(t)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("shell cache-control = %q, want no-cache", got)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("shell response missing ETag")
	}

	// Revalidation with the exact ETag answers 304 with no body.
	conditional := func(inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", inm)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := conditional(etag); rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match exact: status = %d, want 304", rec.Code)
	}
	// A fronting proxy that compresses the shell weakens the ETag to W/"...";
	// RFC 9110 weak comparison must still produce the 304 (a naive string
	// compare here silently kills revalidation behind nginx gzip).
	if rec := conditional("W/" + etag); rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match weakened: status = %d, want 304", rec.Code)
	}
	// ETag lists must match too.
	if rec := conditional(`"stale-etag", ` + etag); rec.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match list: status = %d, want 304", rec.Code)
	}
	// A stale validator gets the full document.
	if rec := conditional(`"stale-etag"`); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("If-None-Match stale: status = %d body = %d bytes, want 200 with body", rec.Code, rec.Body.Len())
	}
}

func TestFrontendStaticFilesCarryValidators(t *testing.T) {
	handler := newFrontendTestHandler(t)

	// Content-hashed bundles are immutable; the URL is the validator.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset cache-control = %q", got)
	}

	// Stable-URL files must revalidate — and the embedded FS has no modtimes,
	// so without an explicit ETag no-cache would force a full re-download on
	// every use (there would be nothing to revalidate against).
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("sw.js cache-control = %q, want no-cache", got)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("stable-path static file missing ETag validator")
	}

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("sw.js If-None-Match: status = %d, want 304", rec.Code)
	}
}
