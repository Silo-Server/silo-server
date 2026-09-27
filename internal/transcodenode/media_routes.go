package transcodenode

import (
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var transcodeNodeMediaRoutes = []streamtelemetry.MediaRoute{
	nodeRoute(http.MethodGet, "/downloads/artifacts/{artifact_id}", streamtelemetry.ClassTransfer),
	nodeRoute(http.MethodHead, "/downloads/artifacts/{artifact_id}", streamtelemetry.ClassTransfer),
	nodeRoute(http.MethodGet, "/remux/{session_id}", streamtelemetry.ClassPlayback),
	nodeRoute(http.MethodHead, "/remux/{session_id}", streamtelemetry.ClassPlayback),
	nodeRoute(http.MethodGet, "/transcode/{session_id}/master.m3u8", streamtelemetry.ClassManifest),
	nodeRoute(http.MethodGet, "/transcode/{session_id}/segment/{name}", streamtelemetry.ClassPlayback),
}

func nodeRoute(method, pattern string, class streamtelemetry.Class) streamtelemetry.MediaRoute {
	return streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyTranscodeNode, Method: method, Pattern: pattern,
		Class: class, Role: streamtelemetry.RoleInternalRelay, CanonicalSessionKey: "transport_session_id",
		CapRelevant: false, Enrolled: true, Capture: nodeCapture(pattern)}
}

func declareTranscodeNodeMediaRoutes() { streamtelemetry.DeclareRoutes(transcodeNodeMediaRoutes...) }

func nodeCapture(pattern string) func(*http.Request) streamtelemetry.CaptureSet {
	return func(r *http.Request) streamtelemetry.CaptureSet {
		// The peer is an API or proxy process authenticated by requireBearer.
		// Recording its address would falsely put a server address in ViewerIPs;
		// a transcode node cannot know viewer identity.
		return streamtelemetry.CaptureSet{Method: r.Method, Pattern: pattern, ReceivedAt: time.Now()}
	}
}

func transcodeNodeMediaRoute(method, pattern string) streamtelemetry.MediaRoute {
	for _, route := range transcodeNodeMediaRoutes {
		if route.Method == method && route.Pattern == pattern {
			return route
		}
	}
	panic("undeclared transcode-node media route: " + method + " " + pattern)
}

func observeNode(registry *streamtelemetry.Registry, method, pattern string, handler http.HandlerFunc) http.HandlerFunc {
	if registry == nil {
		return handler
	}
	return registry.Observe(transcodeNodeMediaRoute(method, pattern))(handler).ServeHTTP
}
