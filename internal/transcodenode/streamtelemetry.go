package transcodenode

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// canonicalSessionID resolves the id the viewer-facing edge publishes this
// session under, so relay bytes merge into that session instead of a phantom
// one. The returned claims are the verified token, or nil, so callers need not
// verify twice.
func (s *Server) canonicalSessionID(r *http.Request, transportID string) (string, *streamtoken.Claims) {
	fallback := "node-transport:" + transportID
	if r == nil || s.watcher == nil {
		return fallback, nil
	}
	tokenStr := r.Header.Get("X-Silo-Stream-Token")
	cfg := s.watcher.Config()
	if tokenStr == "" || cfg == nil {
		return fallback, nil
	}
	claims, err := streamtoken.Verify(tokenStr, cfg.Auth.JWTSecret)
	if err != nil {
		return fallback, nil
	}
	expectedTransportID := claims.SessionID
	if claims.TranscodeTransportID != "" {
		expectedTransportID = claims.TranscodeTransportID
	}
	if expectedTransportID != transportID || claims.SessionID == "" {
		return fallback, nil
	}
	return claims.SessionID, claims
}

func (s *Server) attachTelemetrySession(r *http.Request, transportID string) {
	if r == nil || !streamtelemetry.Observing(r.Context()) {
		return
	}
	sessionID, _ := s.canonicalSessionID(r, transportID)
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{SessionID: sessionID})
}
