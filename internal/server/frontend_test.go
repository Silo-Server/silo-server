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
