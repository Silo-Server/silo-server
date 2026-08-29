package proxy

import (
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var proxyMediaRoutes = []streamtelemetry.MediaRoute{
	proxyRoute(http.MethodGet, "/stream/direct/{token}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodHead, "/stream/direct/{token}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodGet, "/stream/remux/{token}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodHead, "/stream/remux/{token}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodGet, "/stream/remux/audio-v2/{token}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodHead, "/stream/remux/audio-v2/{token}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodGet, "/stream/transcode/{token}/master.m3u8", streamtelemetry.ClassManifest, true),
	proxyRoute(http.MethodHead, "/stream/transcode/{token}/master.m3u8", streamtelemetry.ClassManifest, true),
	proxyRoute(http.MethodGet, "/stream/transcode/{token}/segment/{name}", streamtelemetry.ClassPlayback, true),
	// authorized_media_origins_v1: same viewer egress, different proof of
	// entitlement — a Redis grant plus the caller's own bearer token, never a
	// stream token — so these carry their own canonical session key.
	grantRoute(http.MethodGet, "/stream/v3/{session_id}", streamtelemetry.ClassPlayback, true),
	grantRoute(http.MethodHead, "/stream/v3/{session_id}", streamtelemetry.ClassPlayback, true),
	grantRoute(http.MethodGet, "/stream/v3/{session_id}/master.m3u8", streamtelemetry.ClassManifest, true),
	grantRoute(http.MethodHead, "/stream/v3/{session_id}/master.m3u8", streamtelemetry.ClassManifest, true),
	grantRoute(http.MethodGet, "/stream/v3/{session_id}/segment/{name}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodGet, "/stream/subtitles/{token}/{track}", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodGet, "/stream/subtitles/{token}/{track}/fonts", streamtelemetry.ClassPlayback, true),
	proxyRoute(http.MethodGet, "/downloads/file/{token}", streamtelemetry.ClassTransfer, false),
	proxyRoute(http.MethodHead, "/downloads/file/{token}", streamtelemetry.ClassTransfer, false),
}

func proxyRoute(method, pattern string, class streamtelemetry.Class, capRelevant bool) streamtelemetry.MediaRoute {
	return proxyRouteWithKey(method, pattern, class, capRelevant, "verified_stream_token")
}

// grantRoute declares a credential-free /stream/v3 route. It is viewer egress
// like every other proxy media route; only the session key differs, because the
// identity comes from an authorized grant rather than a verified stream token.
func grantRoute(method, pattern string, class streamtelemetry.Class, capRelevant bool) streamtelemetry.MediaRoute {
	return proxyRouteWithKey(method, pattern, class, capRelevant, "verified_media_grant")
}

func proxyRouteWithKey(method, pattern string, class streamtelemetry.Class, capRelevant bool, sessionKey string) streamtelemetry.MediaRoute {
	return streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyProxy, Method: method, Pattern: pattern,
		Class: class, Role: streamtelemetry.RoleViewerEgress, CanonicalSessionKey: sessionKey,
		CapRelevant: capRelevant, Enrolled: true, Capture: proxyCapture(pattern)}
}

func declareProxyMediaRoutes() { streamtelemetry.DeclareRoutes(proxyMediaRoutes...) }

func proxyCapture(pattern string) func(*http.Request) streamtelemetry.CaptureSet {
	return func(r *http.Request) streamtelemetry.CaptureSet {
		client := playback.ClientInfoFromRequest(r)
		viewerIP := streamtelemetry.ViewerIP(r)
		return streamtelemetry.CaptureSet{
			Method: r.Method, Pattern: pattern, ViewerIP: viewerIP,
			DeviceID:  r.Header.Get("X-Silo-Device-ID"),
			Client:    streamtelemetry.ClientVariant{Name: client.Name, Version: client.Version, Build: client.Build, Channel: client.Channel},
			UserAgent: client.UserAgent, ReceivedAt: time.Now(),
		}
	}
}

func proxyMediaRoute(method, pattern string) streamtelemetry.MediaRoute {
	for _, route := range proxyMediaRoutes {
		if route.Method == method && route.Pattern == pattern {
			return route
		}
	}
	panic("undeclared proxy media route: " + method + " " + pattern)
}

func observeProxy(registry *streamtelemetry.Registry, method, pattern string, handler http.HandlerFunc) http.HandlerFunc {
	if registry == nil {
		return handler
	}
	return registry.Observe(proxyMediaRoute(method, pattern))(handler).ServeHTTP
}
