package jellycompat

import (
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var jellycompatMediaRoutes = []streamtelemetry.MediaRoute{
	compatRoute(http.MethodGet, "/Playback/BitrateTest", streamtelemetry.ClassTransfer, false),
	compatRoute(http.MethodGet, "/Items/{id}/Download", streamtelemetry.ClassTransfer, false),
	compatRoute(http.MethodHead, "/Items/{id}/Download", streamtelemetry.ClassTransfer, false),
	compatRoute(http.MethodGet, "/Videos/{id}/stream", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodHead, "/Videos/{id}/stream", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{id}/stream.{container}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodHead, "/Videos/{id}/stream.{container}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{id}/audio-v2/stream", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodHead, "/Videos/{id}/audio-v2/stream", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{id}/audio-v2/stream.{container}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodHead, "/Videos/{id}/audio-v2/stream.{container}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{id}/master.m3u8", streamtelemetry.ClassManifest, true),
	compatRoute(http.MethodGet, "/Videos/{id}/hls/{playlistId}/stream.m3u8", streamtelemetry.ClassManifest, true),
	compatRoute(http.MethodGet, "/Videos/{id}/hls/{playlistId}/{segmentId}.{segmentContainer}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{id}/audio-v2/master.m3u8", streamtelemetry.ClassManifest, true),
	compatRoute(http.MethodGet, "/Videos/{id}/audio-v2/hls/{playlistId}/stream.m3u8", streamtelemetry.ClassManifest, true),
	compatRoute(http.MethodGet, "/Videos/{id}/audio-v2/hls/{playlistId}/{segmentId}.{segmentContainer}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/stream.{routeFormat}", streamtelemetry.ClassPlayback, true),
	compatRoute(http.MethodGet, "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/{routeDeliveryIndex}/stream.{routeFormat}", streamtelemetry.ClassPlayback, true),
}

func compatRoute(method, pattern string, class streamtelemetry.Class, capRelevant bool) streamtelemetry.MediaRoute {
	return streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyJellycompat, Method: method, Pattern: pattern,
		Class: class, Role: streamtelemetry.RoleViewerEgress, CanonicalSessionKey: "upstream_playback_session",
		CapRelevant: capRelevant, Enrolled: true, Capture: compatCapture(pattern)}
}

// compatCapture records the §2.2 request-time set for a Jellyfin client.
//
// Identity comes from the MediaBrowser authorization header, not X-Silo-Client*:
// Jellyfin clients never send silo's own headers, and firstMediaBrowserAuthorizationValue
// is the parser the negotiation path already uses for DeviceId
// (handlers_playback.go:764), so telemetry reads the same value the play session
// was keyed on rather than a second interpretation of the header.
//
// Byte accounting note: the compat router mounts httpstream.CompressExcept
// globally (router.go:47) and skipCompatMediaCompression (router.go:278) exempts
// the bulk media routes. Wrapping per route puts observedWriter BELOW the
// compression writer, so BytesAccepted equals wire bytes on the exempt routes and
// is PRE-compression on any compat media route still compressed (subtitles) —
// the same rule P0b documents for the native subtitle and font routes. That is
// deliberate; do not "fix" it by moving the wrapper.
func compatCapture(pattern string) func(*http.Request) streamtelemetry.CaptureSet {
	return func(r *http.Request) streamtelemetry.CaptureSet {
		viewerIP := streamtelemetry.ViewerIP(r)
		return streamtelemetry.CaptureSet{
			Method: r.Method, Pattern: pattern, ViewerIP: viewerIP,
			DeviceID: stripCompatNUL(firstMediaBrowserAuthorizationValue(r, "DeviceId")),
			Client: streamtelemetry.ClientVariant{
				Name:    stripCompatNUL(firstMediaBrowserAuthorizationValue(r, "Client")),
				Version: stripCompatNUL(firstMediaBrowserAuthorizationValue(r, "Version")),
			},
			UserAgent: r.UserAgent(), ReceivedAt: time.Now(),
		}
	}
}

func declareJellycompatMediaRoutes() { streamtelemetry.DeclareRoutes(jellycompatMediaRoutes...) }

func compatMediaRoute(method, pattern string) streamtelemetry.MediaRoute {
	for _, route := range jellycompatMediaRoutes {
		if route.Method == method && route.Pattern == pattern {
			return route
		}
	}
	panic("undeclared jellycompat media route: " + method + " " + pattern)
}

// observeCompat wraps a compat media handler. The panic in compatMediaRoute is
// what makes a typo fail the build through the route-manifest test instead of
// silently un-observing a route.
func observeCompat(registry *streamtelemetry.Registry, method, pattern string, handler http.HandlerFunc) http.HandlerFunc {
	if registry == nil {
		return handler
	}
	return registry.Observe(compatMediaRoute(method, pattern))(handler).ServeHTTP
}
