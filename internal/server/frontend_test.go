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
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"assets/app.js":    &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/app.js.br": &fstest.MapFile{Data: []byte("br-compressed")},
		"assets/app.js.gz": &fstest.MapFile{Data: []byte("gzip-compressed")},
		"assets/plain.js":  &fstest.MapFile{Data: []byte("plain")},
		"sw.js":            &fstest.MapFile{Data: []byte("self.addEventListener('fetch', () => {})")},
	}
	t.Cleanup(func() { WebDistFS = prev })
	return FrontendHandler()
}

func TestFrontendPrecompressedAssetNegotiation(t *testing.T) {
	handler := newFrontendTestHandler(t)

	tests := []struct {
		name           string
		acceptEncoding string
		wantEncoding   string
		wantBody       string
	}{
		{
			name:           "brotli wins an equal preference",
			acceptEncoding: "gzip, br",
			wantEncoding:   "br",
			wantBody:       "br-compressed",
		},
		{
			name:           "higher gzip quality wins",
			acceptEncoding: "br;q=0.4, gzip;q=0.8",
			wantEncoding:   "gzip",
			wantBody:       "gzip-compressed",
		},
		{
			name:           "zero quality disables brotli",
			acceptEncoding: "br;q=0, gzip",
			wantEncoding:   "gzip",
			wantBody:       "gzip-compressed",
		},
		{
			name:           "wildcard applies only without an explicit value",
			acceptEncoding: "*;q=0.7, gzip;q=0",
			wantEncoding:   "br",
			wantBody:       "br-compressed",
		},
		{
			name:           "identity when every sidecar is disabled",
			acceptEncoding: "br;q=0, gzip;q=0",
			wantBody:       "console.log(1)",
		},
		{
			name:           "explicit identity overrides a disabled wildcard",
			acceptEncoding: "*;q=0, identity;q=0.5",
			wantBody:       "console.log(1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
			req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Fatalf("content-encoding = %q, want %q", got, tt.wantEncoding)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("vary = %q, want Accept-Encoding", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Fatalf("cache-control = %q", got)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
				t.Fatalf("content-type = %q, want JavaScript", got)
			}
		})
	}
}

func TestFrontendAssetReturnsNotAcceptableWithoutAvailableRepresentation(t *testing.T) {
	handler := newFrontendTestHandler(t)
	tests := []struct {
		name           string
		method         string
		path           string
		acceptEncoding string
		rangeHeader    string
	}{
		{
			name:           "every representation explicitly disabled",
			method:         http.MethodGet,
			path:           "/assets/app.js",
			acceptEncoding: "identity;q=0, br;q=0, gzip;q=0",
		},
		{
			name:           "wildcard disables every representation",
			method:         http.MethodGet,
			path:           "/assets/app.js",
			acceptEncoding: "*;q=0",
		},
		{
			name:           "acceptable sidecars are missing",
			method:         http.MethodGet,
			path:           "/assets/plain.js",
			acceptEncoding: "br, gzip, identity;q=0",
		},
		{
			name:           "range has no acceptable representation",
			method:         http.MethodGet,
			path:           "/assets/app.js",
			acceptEncoding: "br;q=0, gzip;q=0, identity;q=0",
			rangeHeader:    "bytes=0-6",
		},
		{
			name:           "head rejects unavailable representation",
			method:         http.MethodHead,
			path:           "/assets/app.js",
			acceptEncoding: "identity;q=0, br;q=0, gzip;q=0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			if tt.rangeHeader != "" {
				req.Header.Set("Range", tt.rangeHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotAcceptable {
				t.Fatalf("status = %d, want 406", rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("content-encoding = %q, want none", got)
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("vary = %q, want Accept-Encoding", got)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("body = %q, want empty", rec.Body.String())
			}
		})
	}
}

func TestFrontendPrecompressedAssetFallsBackWhenSidecarIsMissing(t *testing.T) {
	handler := newFrontendTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/plain.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "plain" {
		t.Fatalf("response = %d %q, want 200 identity body", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("content-encoding = %q, want identity", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("vary = %q, want Accept-Encoding", got)
	}
}

func TestFrontendPrecompressedAssetTriesNextAcceptableEncoding(t *testing.T) {
	prev := WebDistFS
	WebDistFS = fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html>")},
		"assets/app.js":    &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/app.js.gz": &fstest.MapFile{Data: []byte("gzip-compressed")},
	}
	t.Cleanup(func() { WebDistFS = prev })

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip;q=0.5")
	rec := httptest.NewRecorder()
	FrontendHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "gzip-compressed" {
		t.Fatalf("response = %d %q, want 200 gzip body", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("content-encoding = %q, want gzip", got)
	}
}

func TestFrontendPrecompressedAssetCombinesHeaderLines(t *testing.T) {
	handler := newFrontendTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Add("Accept-Encoding", "gzip;q=0.3")
	req.Header.Add("Accept-Encoding", "br;q=0.8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "br-compressed" {
		t.Fatalf("response = %d %q, want 200 brotli body", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("content-encoding = %q, want br", got)
	}
}

func TestFrontendRangeRequestsUseIdentityAsset(t *testing.T) {
	handler := newFrontendTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	req.Header.Set("Range", "bytes=0-6")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("content-encoding = %q, want identity", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-6/14" {
		t.Fatalf("content-range = %q", got)
	}
	if got := rec.Body.String(); got != "console" {
		t.Fatalf("body = %q, want identity byte range", got)
	}
}

func TestFrontendRangeWithRejectedIdentityUsesFullCompressedAsset(t *testing.T) {
	handler := newFrontendTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "br, identity;q=0")
	req.Header.Set("Range", "bytes=0-6")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("content-encoding = %q, want br", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Fatalf("content-range = %q, want none", got)
	}
	if got := rec.Body.String(); got != "br-compressed" {
		t.Fatalf("body = %q, want full brotli representation", got)
	}
}

func TestFrontendPrecompressedAssetSupportsHead(t *testing.T) {
	handler := newFrontendTestHandler(t)
	req := httptest.NewRequest(http.MethodHead, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("content-encoding = %q, want br", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "13" {
		t.Fatalf("content-length = %q, want 13", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %d bytes, want 0", rec.Body.Len())
	}
}

func TestFrontendPrecompressedAssetIgnoresRangeOnHead(t *testing.T) {
	handler := newFrontendTestHandler(t)
	req := httptest.NewRequest(http.MethodHead, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "br, identity;q=0")
	req.Header.Set("Range", "bytes=0-6")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("content-encoding = %q, want br", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Fatalf("content-range = %q, want none", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %d bytes, want 0", rec.Body.Len())
	}
}

func TestFrontendPrecompressedSidecarsAreNotPublicPaths(t *testing.T) {
	handler := newFrontendTestHandler(t)

	for _, path := range []string{"/assets/app.js.br", "/assets/app.js.gz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
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
