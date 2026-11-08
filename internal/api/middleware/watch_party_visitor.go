package middleware

import (
	"net/http"
	"path"
	"regexp"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// roomRoutePrefix matches the Watch Party room a route names. Visitor routes
// under it must name the visitor's own room; watchPartyVisitorAllowsRoute
// compares the captured id.
const roomRoutePrefix = `^/api/v2/watch-together/rooms/([^/]+)`

// watchPartyVisitorRoomRoutes are the room routes a Watch Party visitor may
// call, for its own room only: reading the room, the live socket, what the
// members have watched of the candidates, suggestions and votes, and
// reporting a source its player refused. Host operations (selection, policy,
// start, stop, promote, close) and creating or joining other rooms are not
// listed, so they stay refused.
var watchPartyVisitorRoomRoutes = []scopeRoute{
	{http.MethodGet, regexp.MustCompile(roomRoutePrefix + `$`)},
	{http.MethodPost, regexp.MustCompile(roomRoutePrefix + `/(member-state|source-fallback|ws-ticket)$`)},
	{http.MethodGet, regexp.MustCompile(roomRoutePrefix + `/suggestions$`)},
	{http.MethodPost, regexp.MustCompile(roomRoutePrefix + `/suggestions$`)},
	{http.MethodDelete, regexp.MustCompile(roomRoutePrefix + `/suggestions/[^/]+$`)},
	{http.MethodPost, regexp.MustCompile(roomRoutePrefix + `/suggestions/[^/]+/vote$`)},
	{http.MethodDelete, regexp.MustCompile(roomRoutePrefix + `/suggestions/[^/]+/vote$`)},
}

// watchPartyVisitorRoutes are the other routes a visitor may call: ending its
// own session, the room capabilities, and playing the room's file. Playback
// start refuses any other file for a visitor (see the playback service), and
// stream and transcode routes still check that the session id is the
// visitor's own playback session. Catalog, browsing, settings, profiles,
// account, admin and every /api/v1 route are absent, so a visitor cannot
// browse or play anything else. A route added later stays closed to visitors
// until it is listed here.
var watchPartyVisitorRoutes = []scopeRoute{
	{http.MethodPost, regexp.MustCompile(`^/api/v2/auth/logout$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/watch-together/capabilities$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/playback/capabilities$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/playback/(start|route-events)$`)},
	{http.MethodDelete, regexp.MustCompile(`^/api/v2/playback/[^/]+$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/playback/[^/]+/(progress|replan)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/playback/transcode/[^/]+/(master\.m3u8|segment/[^/]+)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/stream/[^/]+(/subtitles/[^/]+(/fonts)?)?$`)},
	{http.MethodHead, regexp.MustCompile(`^/api/v2/stream/[^/]+(/subtitles/[^/]+)?$`)},
}

// watchPartyVisitorAllowsRoute reports whether a visitor session bound to
// roomID may call method on urlPath. Like remotePlaybackAllowsRoute it never
// cleans the path and refuses any path that is not already clean.
func watchPartyVisitorAllowsRoute(method, urlPath, roomID string) bool {
	if urlPath == "" || roomID == "" || path.Clean(urlPath) != urlPath {
		return false
	}
	for _, route := range watchPartyVisitorRoomRoutes {
		if route.method != method {
			continue
		}
		if m := route.pattern.FindStringSubmatch(urlPath); m != nil {
			return m[1] == roomID
		}
	}
	for _, route := range watchPartyVisitorRoutes {
		if route.method == method && route.pattern.MatchString(urlPath) {
			return true
		}
	}
	return false
}

// narrowWatchPartyVisitor applies a visitor session's authority to a request
// authenticated with one of its access tokens. It reports false when the
// request must be refused: a route outside the visitor lists, another room,
// or a declared profile other than the host's bound one. Otherwise the
// request acts as the bound profile with the role "user", and the claims
// carry the room and visitor.
func narrowWatchPartyVisitor(r *http.Request, claims *auth.Claims, authority auth.SessionAuthority) bool {
	urlPath, ok := unambiguousRequestPath(r)
	if !ok || !watchPartyVisitorAllowsRoute(r.Method, urlPath, authority.RoomID) {
		return false
	}
	switch r.Header.Get("X-Profile-Id") {
	case "":
		r.Header.Set("X-Profile-Id", authority.BoundProfileID)
	case authority.BoundProfileID:
	default:
		return false
	}
	authority.Narrow(claims)
	return true
}
