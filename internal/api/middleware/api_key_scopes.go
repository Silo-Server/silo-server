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

// userScopeBaseRoutes are admitted by every user scope: who the key is and
// which profiles it may name. A profile-bound key lists only its profile.
var userScopeBaseRoutes = []scopeRoute{
	{http.MethodGet, regexp.MustCompile(`^/api/v2/account/me$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/profiles$`)},
}

func userScope(routes ...scopeRoute) []scopeRoute {
	return append(append([]scopeRoute{}, userScopeBaseRoutes...), routes...)
}

// userEventScope is userScope for a scope in auth.APIKeyEventScopes: it also
// opens the realtime events socket, which then admits only that scope's
// channel.
func userEventScope(routes ...scopeRoute) []scopeRoute {
	return append(userScope(routes...),
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/events/capabilities$`)},
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/events/ws-ticket$`)},
	)
}

var apiKeyScopeRoutes = map[string][]scopeRoute{
	// The user scopes. None lists playback, a stream, a download, a setting,
	// household management or a credential, and GET /watch/{id} stays out
	// because it is the playback plan.
	auth.ScopeCatalogRead: userEventScope(
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/user/libraries(/capabilities)?$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/home/(capabilities|layout|sections(/[^/]+/items)?)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/library/[^/]+/(layout|sections(/[^/]+/items)?|collections(/[^/]+/items)?)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/catalog(/(filters(/search)?|search/capabilities|audiobook-groups|people(/[^/]+)?|works/[^/]+|box-sets(/[^/]+)?|items/[^/]+(/(episodes|versions|box-set))?|series/[^/]+/seasons(/[0-9]+(/episodes)?)?))?$`)},
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/catalog/(query|letters)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/images/capabilities$`)},
	),
	auth.ScopeWatchRead: userEventScope(
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/(history|progress|progress/capabilities)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/sync/progress/(capabilities|snapshots/[^/]+)$`)},
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/sync/progress/snapshots$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/(favorites|ratings|not-interested)(/[^/]+)?$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/(favorites|watchlist)/[^/]+/membership$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/watchlist(/[^/]+)?$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/capabilities/(ratings|not-interested|personal-lists|profile-export)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/profile/export$`)},
	),
	auth.ScopeWatchWrite: userScope(
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/watched/[^/]+$`)},
		scopeRoute{http.MethodDelete, regexp.MustCompile(`^/api/v2/watched/[^/]+$`)},
		// POST /profile/import stays out: it also overwrites the profile's
		// settings and notification preferences, which no user scope reaches.
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/(sync/progress|history/remove)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/(progress/capabilities|sync/progress/capabilities)$`)},
		scopeRoute{http.MethodDelete, regexp.MustCompile(`^/api/v2/progress/[^/]+/position$`)},
		scopeRoute{http.MethodPut, regexp.MustCompile(`^/api/v2/(favorites|ratings|watchlist|not-interested)/[^/]+$`)},
		scopeRoute{http.MethodDelete, regexp.MustCompile(`^/api/v2/(favorites|ratings|watchlist|not-interested)/[^/]+$`)},
	),
	// The inbox only. One notification is not read by ID: its path would
	// also match the delivery-settings reads beside it.
	auth.ScopeNotifications: userEventScope(
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/notifications(/(sync|unread-count|capabilities))?$`)},
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/notifications/(read-all|[^/]+/read)$`)},
	),
	auth.ScopeRequests: userScope(
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/requests/(mine|status|search|[0-9]+)$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/requests/detail/[^/]+/[^/]+$`)},
		scopeRoute{http.MethodGet, regexp.MustCompile(`^/api/v2/requests/discover(/(genres|networks|studios|[^/]+|rows/[^/]+|browse/(genre|network|studio)/[^/]+))?$`)},
		scopeRoute{http.MethodPost, regexp.MustCompile(`^/api/v2/requests(/[0-9]+/cancel)?$`)},
		scopeRoute{http.MethodDelete, regexp.MustCompile(`^/api/v2/requests/[0-9]+$`)},
		scopeRoute{http.MethodPut, regexp.MustCompile(`^/api/v2/requests/follows/[^/]+/[^/]+$`)},
		scopeRoute{http.MethodDelete, regexp.MustCompile(`^/api/v2/requests/follows/[^/]+/[^/]+$`)},
		// Watchlisting a title that is not in the library can create,
		// follow or withdraw a media request, so it belongs here and not to
		// watch:write.
		scopeRoute{http.MethodPut, regexp.MustCompile(`^/api/v2/watchlist/titles/[^/]+/[^/]+$`)},
		scopeRoute{http.MethodDelete, regexp.MustCompile(`^/api/v2/watchlist/titles/[^/]+/[^/]+$`)},
	),
	auth.ScopeAdminSessionsSummaryRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/summary$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/capabilities$`)},
	},
	// Library discovery only, as it was before the user scopes: keys issued
	// with it must not gain the account and profile reads the newer user
	// scopes share.
	auth.ScopeLibrariesRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/user/libraries$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/user/libraries/capabilities$`)},
	},
	auth.ScopeAdminUsers: {
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
	// values, node sessions, request integrations, settings and routes), and
	// node rows are read through their summary. Host resources, download
	// preparations and media requests withhold paths and failure text from
	// scoped keys in their handlers.
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
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/downloads/(entries|devices|storage/capabilities|preparations|preparations/capabilities)$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/system/resources$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/system/resources/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/nodes/summary$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/nodes/capabilities$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/server/status$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/catalog/(items|capabilities)$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/requests$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/requests/(counts|capabilities)$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/requests/[^/]+/events$`)},
	},
	// The catalog listing with file paths, which the listing handler gives
	// only to unscoped callers and keys holding this scope.
	auth.ScopeAdminCatalogPaths: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/catalog/(items|capabilities)$`)},
	},
	auth.ScopeAdminSessionsControl: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/sessions/command-capabilities$`)},
		{http.MethodPost, regexp.MustCompile(`^/api/v2/admin/sessions/[^/]+/(pause|resume|stop|message|terminate)$`)},
	},
	auth.ScopeAdminAccessGroupsRead: {
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/access-groups$`)},
		{http.MethodGet, regexp.MustCompile(`^/api/v2/admin/access-groups/[0-9]+$`)},
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

// bindAPIKeyProfile applies a profile-bound key's binding to the request. It
// reports false when the request must be refused: the key names a profile
// other than its own, or it does not carry user scopes only, which the store
// never writes and is refused rather than trusted. Otherwise the request acts
// as the bound profile, declared for it when it named none, so the viewer gate
// resolves that profile's limits. A key with no binding is left alone.
func bindAPIKeyProfile(r *http.Request, profileID string, scopes []string) bool {
	if profileID == "" {
		return true
	}
	if !auth.UserAPIKeyScopesOnly(scopes) {
		return false
	}
	switch r.Header.Get("X-Profile-Id") {
	case "":
		r.Header.Set("X-Profile-Id", profileID)
	case profileID:
	default:
		return false
	}
	return true
}
