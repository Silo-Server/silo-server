package server

import (
	"io/fs"
	"net/http"
	"strings"
)

// WebDistFS holds the embedded frontend build output.
// When nil, FrontendHandler returns a placeholder response.
var WebDistFS fs.FS

// frontendContentSecurityPolicy is served with every SPA HTML response.
//
// SECURITY: this policy is the primary mitigation for malicious ebook content.
// The in-app reader (foliate-js) renders book chapters in same-origin blob:
// iframes with allow-scripts (required to work around a WebKit bug), and
// blob:/srcdoc documents inherit the embedding document's CSP. With
// script-src 'self', scripts embedded in an EPUB (blob:/inline/data: sources)
// cannot execute, so a hostile book cannot read localStorage tokens or call
// the API. Do not add 'unsafe-inline', 'unsafe-eval', blob:, or data: to
// script-src without revisiting that threat model.
//
// Allowances beyond 'self' exist for concrete app needs:
//   - script-src 'wasm-unsafe-eval': JASSUB (libass) subtitle rendering and
//     node-unrar-js CBR extraction compile WebAssembly.
//   - style-src blob: and 'unsafe-inline': foliate-js loads EPUB stylesheets
//     via blob: URLs; the app uses inline style attributes. Google Fonts CSS
//     is linked from index.html.
//   - img-src/media-src http(s): artwork can come from TMDB/TVDB/S3 public
//     URLs, and stream URLs may point at standalone proxy/transcode workers
//     on another origin (proxy public_url, plain http on LANs).
//   - connect-src http(s)/ws(s): realtime session hub WebSockets, browser-side
//     Plex auth (plex.tv), and HLS fetches against standalone worker origins.
//   - font-src blob: data: plus fonts.gstatic.com for Google Fonts; reader
//     book fonts load from blob: URLs.
const frontendContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline' blob: https://fonts.googleapis.com; " +
	"img-src 'self' blob: data: http: https:; " +
	"font-src 'self' blob: data: https://fonts.gstatic.com; " +
	"media-src 'self' blob: http: https:; " +
	"connect-src 'self' ws: wss: http: https:; " +
	"worker-src 'self' blob:; " +
	"frame-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// FrontendHandler returns an http.Handler that serves the embedded SPA.
// It serves static files from WebDistFS and falls back to index.html for
// SPA routing (any path that doesn't match a file).
func FrontendHandler() http.Handler {
	if WebDistFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Frontend not built. Run: cd web && bun run build"))
		})
	}

	fileServer := http.FileServer(http.FS(WebDistFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		path := r.URL.Path

		// Try to serve the file directly. index.html is excluded so the SPA
		// HTML always goes through the fallback below and carries the CSP.
		if path != "/" && path != "/index.html" && !strings.HasSuffix(path, "/") {
			if f, err := WebDistFS.Open(strings.TrimPrefix(path, "/")); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// SPA fallback: serve index.html
		indexBytes, err := fs.ReadFile(WebDistFS, "index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", frontendContentSecurityPolicy)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	})
}
