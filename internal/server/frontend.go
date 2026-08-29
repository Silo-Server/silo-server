package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
//   - frame-src youtube-nocookie.com: the item-detail trailer modal embeds
//     remote trailers via YouTube's privacy-enhanced iframe host.
const frontendContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline' blob: https://fonts.googleapis.com; " +
	"img-src 'self' blob: data: http: https:; " +
	"font-src 'self' blob: data: https://fonts.gstatic.com; " +
	"media-src 'self' blob: http: https:; " +
	"connect-src 'self' ws: wss: http: https:; " +
	"worker-src 'self' blob:; " +
	"frame-src 'self' blob: https://www.youtube-nocookie.com; " +
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

	// The embedded build output is immutable for the process lifetime, so the
	// unbranded shell can be read once here. A read failure falls back to a
	// per-request error response.
	rawIndex, _ := fs.ReadFile(WebDistFS, "index.html")

	return &frontendHandler{
		fileServer: http.FileServer(http.FS(WebDistFS)),
		rawIndex:   rawIndex,
	}
}

// frontendHandler serves the embedded SPA. It is a struct rather than a
// closure so its caches are scoped to one handler instance and reset when a
// new handler is constructed over a different WebDistFS (as tests do).
type frontendHandler struct {
	fileServer http.Handler
	rawIndex   []byte // unbranded index.html, read once at construction

	// staticETags caches content ETags for stable-URL bundled files (sw.js,
	// icons, vendor bundles). The embedded FS never changes, so a path's ETag
	// is computed at most once.
	staticETags sync.Map // path string -> etag string

	// shell caches the branded index.html and its ETag for the last-seen
	// branding snapshot, so steady-state shell requests — especially the 304
	// revalidations that no-cache makes the common case — skip re-reading,
	// re-rendering, and re-hashing the document.
	shell atomic.Pointer[renderedShell]
}

type renderedShell struct {
	brandingKey string
	body        []byte
	etag        string
}

func (h *frontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	path := r.URL.Path
	if strings.HasPrefix(path, "/assets/") && isPrecompressedAssetPath(path) {
		// Sidecars are implementation details selected through content
		// negotiation. Serving them directly exposes the compressed bytes without
		// Content-Encoding and creates a second public URL for the same asset.
		http.NotFound(w, r)
		return
	}

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
			_ = f.Close()
			if strings.HasPrefix(path, "/assets/") {
				// Vite content-hashes /assets/ filenames, so those URLs are
				// immutable: a new build produces new URLs, which is what
				// lets browsers cache them for a year yet pick up deploys.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				w.Header().Set("Vary", "Accept-Encoding")
				if h.servePrecompressedAsset(w, r, path) {
					return
				}
			} else {
				// Every other bundled file (service worker, icons, vendor
				// bundles) keeps its URL across builds, so it must be
				// revalidated. The embedded FS carries no modtimes, meaning
				// http.FileServer emits no validator of its own — without
				// this ETag, no-cache would force a full re-download on
				// every use instead of a 304.
				w.Header().Set("Cache-Control", "no-cache")
				if etag := h.staticETag(path); etag != "" {
					w.Header().Set("ETag", etag)
				}
			}
			h.fileServer.ServeHTTP(w, r)
			return
		}
	}

	// A missing /assets/ file is a content-hashed chunk from another build,
	// not an app route: answering with the shell makes dynamic imports fail
	// on a text/html module. A 404 surfaces the real condition so the client
	// preload-error handler can reload onto the current build.
	if strings.HasPrefix(path, "/assets/") {
		http.NotFound(w, r)
		return
	}

	// SPA fallback: serve the (branded) index.html shell.
	shell, ok := h.brandedShell(r)
	if !ok {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", frontendContentSecurityPolicy)
	// The HTML shell keeps a stable URL across builds, so it must never be
	// served stale: every deploy changes which content-hashed /assets/*
	// bundles it references. no-cache lets browsers and CDNs store it but
	// forces revalidation on each load; the ETag turns an unchanged shell
	// into a cheap 304. ServeContent implements RFC 9110 conditional
	// semantics (weak comparison, ETag lists), so revalidation keeps working
	// behind proxies that compress the body and weaken the ETag to W/"...".
	w.Header().Set("ETag", shell.etag)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(shell.body))
}

func isPrecompressedAssetPath(path string) bool {
	return strings.HasSuffix(path, ".js.br") ||
		strings.HasSuffix(path, ".js.gz") ||
		strings.HasSuffix(path, ".css.br") ||
		strings.HasSuffix(path, ".css.gz")
}

const (
	brotliContentEncoding = "br"
	gzipContentEncoding   = "gzip"
)

func (h *frontendHandler) servePrecompressedAsset(
	w http.ResponseWriter,
	r *http.Request,
	assetPath string,
) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	acceptEncoding := strings.Join(r.Header.Values("Accept-Encoding"), ",")
	encodings, identityAcceptable := precompressedEncodingPreferences(acceptEncoding)
	useIdentityRange := r.Method == http.MethodGet && r.Header.Get("Range") != "" && identityAcceptable
	if !useIdentityRange {
		for _, encoding := range encodings {
			suffix := "." + encoding
			if encoding == gzipContentEncoding {
				suffix = ".gz"
			}
			sidecarPath := assetPath + suffix
			f, err := WebDistFS.Open(strings.TrimPrefix(sidecarPath, "/"))
			if err != nil {
				continue
			}
			info, statErr := f.Stat()
			_ = f.Close()
			if statErr != nil || info.IsDir() {
				continue
			}

			if contentType := mime.TypeByExtension(pathpkg.Ext(assetPath)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.Header().Set("Content-Encoding", encoding)
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

			encodedRequest := r.Clone(r.Context())
			encodedURL := *r.URL
			encodedURL.Path = sidecarPath
			encodedRequest.URL = &encodedURL
			if encodedRequest.Header.Get("Range") != "" {
				// Range is ignored on HEAD and when GET cannot use identity.
				encodedRequest.Header.Del("Range")
			}
			h.fileServer.ServeHTTP(w, encodedRequest)
			return true
		}
	}
	if !identityAcceptable {
		w.WriteHeader(http.StatusNotAcceptable)
		return true
	}
	return false
}

func precompressedEncodingPreferences(header string) ([]string, bool) {
	qualities := make(map[string]float64)
	wildcardQuality := -1.0

	for value := range strings.SplitSeq(header, ",") {
		parts := strings.Split(value, ";")
		coding := strings.ToLower(strings.TrimSpace(parts[0]))
		if coding == "" {
			continue
		}

		quality := 1.0
		valid := true
		for _, parameter := range parts[1:] {
			key, rawValue, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			if !found {
				valid = false
				break
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
			if err != nil || parsed < 0 || parsed > 1 {
				valid = false
				break
			}
			quality = parsed
		}
		if !valid {
			continue
		}

		if coding == "*" {
			wildcardQuality = quality
			continue
		}
		qualities[coding] = quality
	}

	qualityFor := func(coding string) float64 {
		if quality, ok := qualities[coding]; ok {
			return quality
		}
		if wildcardQuality >= 0 {
			return wildcardQuality
		}
		return 0
	}

	brotliQuality := qualityFor(brotliContentEncoding)
	gzipQuality := qualityFor(gzipContentEncoding)
	identityAcceptable := true
	if identityQuality, ok := qualities["identity"]; ok {
		identityAcceptable = identityQuality > 0
	} else if wildcardQuality == 0 {
		identityAcceptable = false
	}
	if brotliQuality <= 0 && gzipQuality <= 0 {
		return nil, identityAcceptable
	}

	encodings := make([]string, 0, 2)
	if brotliQuality >= gzipQuality {
		if brotliQuality > 0 {
			encodings = append(encodings, brotliContentEncoding)
		}
		if gzipQuality > 0 {
			encodings = append(encodings, gzipContentEncoding)
		}
		return encodings, identityAcceptable
	}
	if gzipQuality > 0 {
		encodings = append(encodings, gzipContentEncoding)
	}
	if brotliQuality > 0 {
		encodings = append(encodings, brotliContentEncoding)
	}
	return encodings, identityAcceptable
}

// brandedShell returns the branding-rendered index.html and its ETag, reusing
// the cached rendering while the branding snapshot is unchanged.
func (h *frontendHandler) brandedShell(r *http.Request) (*renderedShell, bool) {
	if h.rawIndex == nil {
		return nil, false
	}
	var brandingKey string
	var snap branding.Snapshot
	if Branding != nil {
		snap = Branding.Load(r.Context())
		brandingKey = snap.RenderKey()
	}
	if cached := h.shell.Load(); cached != nil && cached.brandingKey == brandingKey {
		return cached, true
	}
	body := h.rawIndex
	if Branding != nil {
		body = branding.RenderIndexHTML(h.rawIndex, snap)
	}
	rendered := &renderedShell{brandingKey: brandingKey, body: body, etag: contentETag(body)}
	h.shell.Store(rendered)
	return rendered, true
}

// staticETag returns the content ETag for a bundled static file, computing and
// caching it on first use. Returns "" for paths that can't be read as files
// (directories), which are served without a validator.
func (h *frontendHandler) staticETag(path string) string {
	if v, ok := h.staticETags.Load(path); ok {
		if etag, ok := v.(string); ok {
			return etag
		}
	}
	data, err := fs.ReadFile(WebDistFS, strings.TrimPrefix(path, "/"))
	if err != nil {
		return ""
	}
	etag := contentETag(data)
	h.staticETags.Store(path, etag)
	return etag
}

// contentETag derives a strong validator from response bytes so a no-cache
// resource can answer conditional requests with a 304 instead of re-sending the
// body. Truncated SHA-256 is ample for cache validation (not a security token).
func contentETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
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
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", branding.AssetContentSecurityPolicy)
	w.Header().Set("ETag", `"`+ref+`"`)
	// Stable path (no content hash in the URL), so revalidate rather than cache
	// long-lived; the ETag lets browsers skip the body when unchanged.
	// ServeContent handles If-None-Match with RFC 9110 semantics (weak
	// comparison, ETag lists) rather than a naive string compare.
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	return true
}
