package plugins

import (
	"net/http"
	"strconv"
	"strings"
)

// ContentPrefix is the common parent for versioned plugin pages and assets.
// Launch cookies must remain scoped to this parent, never the entire API.
const ContentPrefix = "/api/v2/plugin-content"

const contentPages = "plugins"

type ContentAccess struct {
	Authenticated bool
	Admin         bool
	UserID        int
	ProfileID     string
	// RenewedCookie, when set, replaces the plugin launch cookie on the
	// response, so an open plugin page outlives the cookie's five minutes
	// while its login session lasts.
	RenewedCookie *http.Cookie
}

type ContentAccessResolver func(*http.Request) ContentAccess

// WithRenewedCookie sets a renewed plugin launch cookie on w and returns a
// writer that marks the response private, no-store as its headers are
// written. Whatever answers the request (a plugin with its own
// Cache-Control, a static page, or http.ServeFile's error path, which drops
// Cache-Control), a shared cache in front of Silo never stores one viewer's
// credential for another. A nil cookie returns w unchanged.
func WithRenewedCookie(w http.ResponseWriter, cookie *http.Cookie) http.ResponseWriter {
	if cookie == nil {
		return w
	}
	http.SetCookie(w, cookie)
	return &noStoreWriter{ResponseWriter: w}
}

type noStoreWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *noStoreWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.Header().Set("Cache-Control", "private, no-store")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *noStoreWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *noStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ContentHandler adapts only the versioned mount. The proxy and bridge remain
// responsible for route matching, plugin access policy and forwarded bytes.
type ContentHandler struct {
	proxy       *HTTPProxy
	routeAccess ContentAccessResolver
	assetAccess ContentAccessResolver
}

func NewContentHandler(proxy *HTTPProxy, routes, assets ContentAccessResolver) *ContentHandler {
	return &ContentHandler{proxy: proxy, routeAccess: routes, assetAccess: assets}
}

func (h *ContentHandler) ContentAvailable() bool { return h != nil && h.proxy != nil }

func (h *ContentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.ContentAvailable() {
		http.Error(w, "plugin content unavailable", http.StatusServiceUnavailable)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, ContentPrefix+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	kind, rest, ok := strings.Cut(rest, "/")
	if !ok || (kind != contentPages && kind != "plugin-assets") {
		http.NotFound(w, r)
		return
	}
	id, subpath, hasSlash := strings.Cut(rest, "/")
	installationID, err := strconv.Atoi(id)
	if err != nil {
		http.Error(w, "invalid installation id", http.StatusBadRequest)
		return
	}
	if kind == contentPages && !hasSlash {
		// A canonical directory URL is required for browser-relative SPA assets.
		target := *r.URL
		target.Path += "/"
		if target.RawPath != "" {
			target.RawPath += "/"
		}
		http.Redirect(w, r, target.RequestURI(), http.StatusPermanentRedirect)
		return
	}
	access := ContentAccess{}
	if kind == contentPages {
		if h.routeAccess != nil {
			access = h.routeAccess(r)
		}
		w = WithRenewedCookie(w, access.RenewedCookie)
		ctx := WithPluginAccessUser(r.Context(), access.Authenticated, access.Admin, access.UserID, access.ProfileID)
		h.proxy.serveRoute(w, r.WithContext(ctx), installationID, access.Authenticated, access.Admin, true)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subpath = strings.TrimPrefix(subpath, "/")
	if subpath == "" {
		http.NotFound(w, r)
		return
	}
	if h.assetAccess != nil {
		access = h.assetAccess(r)
	}
	h.proxy.ServeAsset(w, r.WithContext(WithPluginAccess(r.Context(), access.Authenticated, access.Admin)), installationID, subpath)
}
