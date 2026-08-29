package api

import (
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var nativeMediaRoutes = []streamtelemetry.MediaRoute{
	nativeRoute(http.MethodGet, "/api/v1/stream/{session_id}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodHead, "/api/v1/stream/{session_id}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodGet, "/api/v1/stream/{session_id}/subtitles/{track}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodHead, "/api/v1/stream/{session_id}/subtitles/{track}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodGet, "/api/v1/stream/{session_id}/subtitles/{track}/fonts", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodGet, "/api/v1/playback/transcode/{session_id}/master.m3u8", streamtelemetry.ClassManifest, true),
	nativeRoute(http.MethodGet, "/api/v1/playback/transcode/{session_id}/segment/{name}", streamtelemetry.ClassPlayback, true),
	nativeRoute(http.MethodGet, "/api/v1/downloads/{id}/file", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v1/downloads/{id}/file", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v1/downloads/{id}/file-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v1/downloads/{id}/file-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v1/downloads/{id}/subtitles/{ref}", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v1/direct-download", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v1/direct-download", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v1/direct-download-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v1/direct-download-proxy", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodGet, "/api/v1/ebooks/{content_id}/files/{file_id}/read", streamtelemetry.ClassTransfer, false),
	nativeRoute(http.MethodHead, "/api/v1/ebooks/{content_id}/files/{file_id}/read", streamtelemetry.ClassTransfer, false),
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
		}
	}
}

func declareNativeMediaRoutes() { streamtelemetry.DeclareRoutes(nativeMediaRoutes...) }

func nativeMediaRoute(method, pattern string) streamtelemetry.MediaRoute {
	for _, route := range nativeMediaRoutes {
		if route.Method == method && route.Pattern == pattern {
			return route
		}
	}
	panic("undeclared native media route: " + method + " " + pattern)
}

func observeNative(registry *streamtelemetry.Registry, method, pattern string, handler http.HandlerFunc) http.HandlerFunc {
	if registry == nil {
		return handler
	}
	return registry.Observe(nativeMediaRoute(method, pattern))(handler).ServeHTTP
}
