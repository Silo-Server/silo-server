package middleware

import (
	"net/http"
	"path"
	"regexp"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// remotePlaybackRoutes are the only routes a remote-playback handoff session
// may call: playing what the approver sent to the device, the bound
// profile's reads around it (next up, browsing, subtitles, settings), and the
// track choices it saves. The Apple and Android TV apps send every request on
// the handoff's credentials while it lasts, so this is what they call.
// Everything else is refused: admin and account management, household
// management, other profiles, and every frozen /api/v1 route. A route added
// later stays closed to handoffs until it is listed here. The usual viewer,
// profile and playback ownership gates still apply after this list. See
// docs/architecture/device-login.md#remote-playback-handoff.
var remotePlaybackRoutes = []scopeRoute{
	// The session itself.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/account/me$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/auth/logout$`)},
	// GET /profiles lists only the bound profile to a handoff.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/profiles$`)},
	// Playback, its control socket and its media.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/playback/capabilities$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/playback/(start|route-events)$`)},
	{http.MethodDelete, regexp.MustCompile(`^/api/v2/playback/[^/]+$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/playback/[^/]+/(progress|replan)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/playback/transcode/[^/]+/(master\.m3u8|segment/[^/]+)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/playback/sessions/control/capabilities$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/playback/sessions/[^/]+/control/ws-ticket$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/stream/[^/]+(/subtitles/[^/]+(/fonts)?)?$`)},
	{http.MethodHead, regexp.MustCompile(`^/api/v2/stream/[^/]+(/subtitles/[^/]+)?$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/watch/[^/]+(/trickplay)?$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/markers/(items|files)/[^/]+$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/sync/progress/(capabilities|snapshots/[^/]+)$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/sync/progress(/snapshots)?$`)},
	// The bound profile's own track choices.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/(audio|subtitle)-prefs/[^/]+$`)},
	{http.MethodPut, regexp.MustCompile(`^/api/v2/(audio|subtitle)-prefs/[^/]+$`)},
	{http.MethodDelete, regexp.MustCompile(`^/api/v2/(audio|subtitle)-prefs/[^/]+$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/library-playback-prefs$`)},
	// Subtitles found or made while playing, and their sync and timing.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/subtitles/(providers/status|sync/status|ai/(status|quota|jobs(/[^/]+)?)|[0-9]+)$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/subtitles/(search|download|detect-language|ai/translate|ai/jobs/[^/]+/cancel)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/subtitles/([0-9]+/sync(/[^/]+)?|stored/[^/]+/(sync|metadata))$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/subtitles/([0-9]+/sync/[^/]+|stored/[^/]+/sync)$`)},
	{http.MethodPut, regexp.MustCompile(`^/api/v2/subtitles/([0-9]+/sync/[^/]+|stored/[^/]+)/timing$`)},
	// Next up, and browsing the bound profile's libraries on the device.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/home/(capabilities|layout|sections(/[^/]+/items)?)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/catalog(/(filters(/search)?|search/capabilities|people(/[^/]+)?|works/[^/]+|items/[^/]+(/(episodes|versions))?|series/[^/]+/seasons(/[0-9]+(/episodes)?)?))?$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/catalog/query$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/library/[^/]+/(layout|sections(/[^/]+/items)?|collections(/[^/]+/items)?)$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/user/libraries(/capabilities)?$`)},
	{http.MethodGet, regexp.MustCompile(`^/api/v2/images/capabilities$`)},
	// Settings the player reads; writes are refused.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/settings/(contract(/capabilities)?|values/effective|subtitle-appearance/effective|overlay-config)$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/settings/values/effective$`)},
	// The events socket the TV apps keep open.
	{http.MethodGet, regexp.MustCompile(`^/api/v2/events/capabilities$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v2/events/ws-ticket$`)},
}

// remotePlaybackAllowsRoute reports whether a remote-playback session may
// call method on urlPath. Unlike API key scopes it never cleans the path: a
// `..` or doubled-slash spelling could clean to a listed route while the
// router serves another, so any path that is not already clean is refused.
func remotePlaybackAllowsRoute(method, urlPath string) bool {
	if urlPath == "" || path.Clean(urlPath) != urlPath {
		return false
	}
	for _, route := range remotePlaybackRoutes {
		if route.method == method && route.pattern.MatchString(urlPath) {
			return true
		}
	}
	return false
}

// narrowRemotePlayback applies a remote-playback session's authority to a
// request authenticated with one of its access tokens. It reports false when
// the request must be refused: a route outside remotePlaybackRoutes, or a
// declared profile other than the bound one. Otherwise the request acts as
// the bound profile (declared for it when it named none, so the viewer gate
// resolves that profile and its PIN proof) and the claims carry the account
// role "user" and are not the account's own login session.
func narrowRemotePlayback(r *http.Request, claims *auth.Claims, authority auth.SessionAuthority) bool {
	// The router matches the escaped path when it differs from the default
	// encoding (an encoded slash, say), so such a path is refused rather
	// than judged by its decoded form.
	if r.URL.RawPath != "" || !remotePlaybackAllowsRoute(r.Method, r.URL.Path) {
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
