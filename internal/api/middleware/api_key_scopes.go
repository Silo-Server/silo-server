package middleware

import (
	"net/http"
	"path"
	"regexp"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// A scoped API key is an allowlist credential: it may only call the routes
// its scopes name, and is refused everywhere else. Enforcement happens in
// RequireAuth (before routing-group middleware), so the allowlist is written
// against the public URL surface rather than the chi route tree — a route
// added anywhere in the API is denied to scoped keys until it is explicitly
// listed here. Scopes never grant: role middleware (admin-only routes) still
// applies to the key's owning user afterwards.

// scopeRoute names one method+path a scope admits.
type scopeRoute struct {
	method  string
	pattern *regexp.Regexp
}

var apiKeyScopeRoutes = map[string][]scopeRoute{
	auth.ScopeAdminSessionsSummaryRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/summary$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/capabilities$`)},
	},
	auth.ScopeLibrariesRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/user/libraries$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/user/libraries/capabilities$`)},
	},
	auth.ScopeAdminUsers: {
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/users$`)},
		{http.MethodPost, regexp.MustCompile(`^/api/v1/admin/users$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+$`)},
		{http.MethodPut, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+$`)},
		{http.MethodDelete, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+/profiles$`)},
		// V2 preserves the same scoped account administration surface.
		{http.MethodPost, regexp.MustCompile(`^/api/v2/admin/users$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/policy-defaults$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/[0-9]+$`)},
		{http.MethodPut, regexp.MustCompile(`^/api/v2/admin/users/[0-9]+$`)},
		{http.MethodDelete, regexp.MustCompile(`^/api/v2/admin/users/[0-9]+$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/[0-9]+/profiles$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users$`)},
	},
	// Monitoring reads only. Operations that carry paths, node addresses,
	// storage keys, setting values or credentials stay off this list (item
	// files, storage, settings, logs, API keys, audit, per-device setting
	// values, node sessions), and node rows are read through their summary.
	// Host resources and download preparations withhold paths and failure
	// text from scoped keys in their handlers.
	auth.ScopeAdminMonitorRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/command-capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/summary$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/events/capabilities$`)},
		// The one write: a ticket for the read-only events socket.
		{http.MethodPost, regexp.MustCompile(`^/api/v2/events/ws-ticket$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/playback-history$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/playback-history/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/stats/(timeseries|playback-activity|top-activity|downloads)$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/[0-9]+$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/users/[0-9]+/(profiles|devices|ips|downloads|downloads/summary|watch-summary)$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/devices$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/devices/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/ips$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/downloads/(entries|devices|preparations|preparations/capabilities)$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/system/resources$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/system/resources/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/nodes/summary$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/nodes/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/server/status$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/catalog/(items|capabilities)$`)},
	},
	auth.ScopeAdminSessionsControl: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/command-capabilities$`)},
		{http.MethodPost, regexp.MustCompile(`^/api/v2/admin/sessions/[^/]+/(pause|resume|stop|message|terminate)$`)},
	},
	auth.ScopeAdminAccessGroupsRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/access-groups$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/access-groups/[0-9]+$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/access-groups$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/access-groups/[0-9]+$`)},
	},
}

// apiKeyScopesAllow reports whether a key carrying scopes may perform the
// request. Unscoped keys (empty scopes) are always allowed — they keep the
// pre-scopes behavior. A scoped key's path must be one the router reads the
// same way the allowlist does (see unambiguousRequestPath); any other
// spelling is refused rather than judged by a normalized form.
func apiKeyScopesAllow(scopes []string, r *http.Request) bool {
	if len(scopes) == 0 {
		return true
	}
	requestPath, ok := unambiguousRequestPath(r)
	if !ok {
		return false
	}
	for _, scope := range scopes {
		for _, route := range apiKeyScopeRoutes[scope] {
			if route.method == r.Method && route.pattern.MatchString(requestPath) {
				return true
			}
		}
	}
	return false
}

// unambiguousRequestPath returns r.URL.Path when a route allowlist and the
// router are certain to read it as the same path. chi matches r.URL.RawPath
// whenever it is set (an encoded slash or dot, say) and otherwise the path
// exactly as sent, without cleaning it, so judging a decoded or cleaned form
// could admit one route while the router serves another: `..%2Fusers%2F1`
// decodes and cleans to a users route but routes as one opaque segment. ok is
// false for an escaped path, an empty one, or one with dot segments,
// doubled slashes or a trailing slash.
func unambiguousRequestPath(r *http.Request) (p string, ok bool) {
	p = r.URL.Path
	return p, r.URL.RawPath == "" && p != "" && path.Clean(p) == p
}
