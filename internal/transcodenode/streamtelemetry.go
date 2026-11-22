package transcodenode

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// playbackSessionID is the id the viewer-facing edge publishes the transcode
// under transportID as: the playback session the start request or the core's
// resolution named. A session started without one files its bytes under a
// node-only id rather than merging them into a session they may not belong
// to.
func (s *Server) playbackSessionID(transportID string) string {
	s.mu.RLock()
	identity := s.identities[transportID]
	s.mu.RUnlock()
	if identity.playbackSessionID != "" {
		return identity.playbackSessionID
	}
	return "node-transport:" + transportID
}

// attachTelemetrySession files the request's bytes under the playback session
// the viewer-facing edge publishes, so relay bytes merge into that session
// instead of a phantom one.
func (s *Server) attachTelemetrySession(r *http.Request, transportID string) {
	if r == nil || !streamtelemetry.Observing(r.Context()) {
		return
	}
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{SessionID: s.playbackSessionID(transportID)})
}
