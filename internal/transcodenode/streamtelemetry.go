package transcodenode

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// attachTelemetrySession files the request's bytes under the playback session
// the viewer-facing edge publishes, so relay bytes merge into that session
// instead of a phantom one. The id comes from the start request or the core's
// resolution, never from a token.
func attachTelemetrySession(r *http.Request, playbackSessionID string) {
	if r == nil || playbackSessionID == "" || !streamtelemetry.Observing(r.Context()) {
		return
	}
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{SessionID: playbackSessionID})
}
