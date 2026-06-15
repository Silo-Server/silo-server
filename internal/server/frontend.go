package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/branding"
)

// WebDistFS holds the embedded frontend build output.
// When nil, FrontendHandler returns a placeholder response.
var WebDistFS fs.FS

// Branding supplies white-label customization (server name, favicon, manifest)
// to the SPA shell. When nil, the frontend is served exactly as built.
var Branding *branding.Service

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

		// Dynamic branding endpoints must be handled before the static file
		// server, which would otherwise serve the bundled defaults shadowing
		// them. Both fall through to the static asset when no override applies.
		if Branding != nil {
			switch path {
			case "/site.webmanifest":
				serveDynamicManifest(w, r)
				return
			case "/favicon.ico":
				if serveCustomFavicon(w, r) {
					return
				}
			}
		}

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
		if Branding != nil {
			indexBytes = branding.RenderIndexHTML(indexBytes, Branding.Load(r.Context()))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", frontendContentSecurityPolicy)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	})
}

// serveDynamicManifest writes the branding-aware web app manifest.
func serveDynamicManifest(w http.ResponseWriter, r *http.Request) {
	body := branding.RenderManifest(Branding.Load(r.Context()))
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveCustomFavicon serves the admin-uploaded favicon at /favicon.ico when one
// is configured, so direct requests (browsers, crawlers) get the branded icon.
// Returns false when there is no custom favicon, letting the caller fall through
// to the bundled static file.
func serveCustomFavicon(w http.ResponseWriter, r *http.Request) bool {
	data, contentType, ref, err := Branding.GetAsset(r.Context(), branding.KindFavicon)
	if err != nil {
		return false
	}
	// X-Content-Type-Options is already set on the response by the caller. The
	// favicon may be an admin-uploaded SVG; harden it against script execution
	// on direct navigation (stored-XSS defense), matching the API asset route.
	etag := `"` + ref + `"`
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", branding.AssetContentSecurityPolicy)
	w.Header().Set("ETag", etag)
	// Stable path (no content hash in the URL), so revalidate rather than cache
	// long-lived; the ETag lets browsers skip the body when unchanged.
	w.Header().Set("Cache-Control", "public, max-age=300")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return true
}
