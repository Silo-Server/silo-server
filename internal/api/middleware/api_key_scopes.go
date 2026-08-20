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
	auth.ScopeAdminUsers: {
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/users$`)},
		{http.MethodPost, regexp.MustCompile(`^/api/v1/admin/users$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+$`)},
		{http.MethodPut, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+$`)},
		{http.MethodDelete, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/users/[0-9]+/profiles$`)},
	},
	auth.ScopeAdminAccessGroupsRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/access-groups$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v1/admin/access-groups/[0-9]+$`)},
	},
}

// apiKeyScopesAllow reports whether a key carrying scopes may perform the
// request. Unscoped keys (empty scopes) are always allowed — they keep the
// pre-scopes behavior. The path is cleaned before matching so `..` or
// duplicate-slash spellings cannot dodge the anchored patterns; matching uses
// the cleaned path only and never rewrites the request.
func apiKeyScopesAllow(scopes []string, r *http.Request) bool {
	if len(scopes) == 0 {
		return true
	}
	requestPath := path.Clean("/" + r.URL.Path)
	for _, scope := range scopes {
		for _, route := range apiKeyScopeRoutes[scope] {
			if route.method == r.Method && route.pattern.MatchString(requestPath) {
				return true
			}
		}
	}
	return false
}
