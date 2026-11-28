package api

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var nativeMediaRoutes = []streamtelemetry.MediaRoute{
	nativeRoute(http.MethodGet, "/api/v2/catalog/items/{id}/themes/{theme_id}/audio", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v2/catalog/items/{id}/themes/{theme_id}/audio", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v2/downloads/{id}/file", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v2/downloads/{id}/file", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v2/downloads/{id}/file-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v2/downloads/{id}/file-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v2/downloads/{id}/subtitles/{ref}", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v2/direct-download", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v2/playback/bandwidth-probe", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v2/direct-download", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v2/direct-download-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v2/direct-download-proxy", streamtelemetry.ClassTransfer, false),
}

func nativeRoute(method, pattern string, class streamtelemetry.Class, capRelevant bool) streamtelemetry.MediaRoute {
	return streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: method, Pattern: pattern,
		Class: class, Role: streamtelemetry.RoleViewerEgress, CanonicalSessionKey: "handler_attachment",
		CapRelevant: capRelevant, Enrolled: true, Capture: nativeCapture(pattern)}
}

func nativeCapture(pattern string) func(*http.Request) streamtelemetry.CaptureSet {
	return func(r *http.Request) streamtelemetry.CaptureSet {
		client := playback.ClientInfoFromRequest(r)
		viewerIP := streamtelemetry.ViewerIP(r)
		return streamtelemetry.CaptureSet{
			Method: r.Method, Pattern: pattern, ViewerIP: viewerIP,
			DeviceID:  r.Header.Get("X-Silo-Device-ID"),
			Client:    streamtelemetry.ClientVariant{Name: client.Name, Version: client.Version, Build: client.Build, Channel: client.Channel},
			UserAgent: client.UserAgent, ReceivedAt: time.Now(),
			Transport: directTransport(r),
		}
	}
}

// directTransport describes a request that arrived over a direct path; a
// request on any other path has the zero value.
func directTransport(r *http.Request) streamtelemetry.Transport {
	direct := netaccess.PathFromContext(r.Context()).Direct
	if direct == nil {
		return streamtelemetry.Transport{}
	}
	transport := streamtelemetry.Transport{PathKind: streamtelemetry.PathKindDirect, Class: direct.Class, HostID: direct.HostID,
		LocalPort: int(direct.LocalPort), PathGeneration: direct.PathGeneration}
	if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		transport.PeerPort = int(peer.Port())
		transport.AddressFamily = "ipv6"
		if peer.Addr().Unmap().Is4() {
			transport.AddressFamily = "ipv4"
		}
	}
	return transport
}

var playbackV2MediaRoutes = []streamtelemetry.MediaRoute{
	nativeRoute(http.MethodGet, "/api/v2/stream/{session_id}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodHead, "/api/v2/stream/{session_id}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodGet, "/api/v2/playback/transcode/{session_id}/master.m3u8", streamtelemetry.ClassManifest, true),
	nativeRoute(http.MethodHead, "/api/v2/playback/transcode/{session_id}/master.m3u8", streamtelemetry.ClassManifest, true),
	nativeRoute(http.MethodGet, "/api/v2/playback/transcode/{session_id}/segment/{name}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodGet, "/api/v2/stream/{session_id}/subtitles/{track}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodHead, "/api/v2/stream/{session_id}/subtitles/{track}", streamtelemetry.ClassPlayback, true),
}

func declareNativeMediaRoutes() {
	streamtelemetry.DeclareRoutes(nativeMediaRoutes...)
	streamtelemetry.DeclareRoutes(playbackV2MediaRoutes...)
}

func nativeMediaRoute(method, pattern string) streamtelemetry.MediaRoute {
	for _, routes := range [][]streamtelemetry.MediaRoute{nativeMediaRoutes, playbackV2MediaRoutes} {
		for _, route := range routes {
			if route.Method == method && route.Pattern == pattern {
				return route
			}
		}
	}
	panic("undeclared native media route: " + method + " " + pattern)
}

// methodHandlers serves HEAD with head and every other method with get, so
// each is wrapped once when the router is built.
func methodHandlers(get, head http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			head(w, r)
			return
		}
		get(w, r)
	}
}

func observeNative(registry *streamtelemetry.Registry, method, pattern string, handler http.HandlerFunc) http.HandlerFunc {
	if registry == nil {
		return handler
	}
	return registry.Observe(nativeMediaRoute(method, pattern))(handler).ServeHTTP
}
