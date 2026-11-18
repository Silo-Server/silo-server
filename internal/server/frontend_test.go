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

	for _, path := range []string{"/", "/index.html", "/library/ebooks", "/reset-password/old-form-token"} {
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
			// A link in the old path form must not hand its token to every
			// asset and API request as a Referer.
			if got := rr.Header().Get("Referrer-Policy"); got != "strict-origin" {
				t.Fatalf("referrer-policy = %q", got)
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

func newFrontendTestHandler(t *testing.T) http.Handler {
	t.Helper()
	prev := WebDistFS
	WebDistFS = fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html><div id=\"root\"></div>")},
		"assets/app.js":    &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/app.js.br": &fstest.MapFile{Data: []byte("br-compressed")},
		"assets/app.js.gz": &fstest.MapFile{Data: []byte("gzip-compressed")},
		"assets/plain.js":  &fstest.MapFile{Data: []byte("plain")},
		// WASM gets sidecars too: the JASSUB subtitle renderer is ~2 MB raw.
		"assets/sub.wasm":    &fstest.MapFile{Data: []byte("\x00asm-identity")},
		"assets/sub.wasm.br": &fstest.MapFile{Data: []byte("wasm-br")},
		"assets/sub.wasm.gz": &fstest.MapFile{Data: []byte("wasm-gzip")},
		"sw.js":              &fstest.MapFile{Data: []byte("self.addEventListener('fetch', () => {})")},
	}
	t.Cleanup(func() { WebDistFS = prev })
	return FrontendHandler()
}

func TestFrontendPrecompressedSidecarsAreNotPublicPaths(t *testing.T) {
	handler := newFrontendTestHandler(t)

	for _, path := range []string{
		"/assets/app.js.br",
		"/assets/app.js.gz",
		"/assets/sub.wasm.br",
		"/assets/sub.wasm.gz",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
}
