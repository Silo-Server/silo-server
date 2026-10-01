package playback

import (
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// TelemetryTokenTiming resolves the timing a viewer-edge handler attaches from
// a verified stream token. It never invents a start time: a token with no usable
// timestamp yields a zero time and StartedAtSourceFirstSeen, and the caller
// supplies its own fallback if it has a better one.
func TelemetryTokenTiming(claims *streamtoken.Claims) (
	startedAt time.Time,
	startedSource streamtelemetry.StartedAtSource,
	tokenIssuedAt time.Time,
	tokenSource streamtelemetry.TokenIssuedAtSource,
) {
	startedSource = streamtelemetry.StartedAtSourceFirstSeen
	tokenSource = streamtelemetry.TokenIssuedAtSourceNone
	if claims == nil {
		return
	}
	if resolved, source := claims.StartedAt(); !resolved.IsZero() {
		startedAt = resolved
		switch source {
		case streamtoken.StartedAtSourceClaim:
			startedSource = streamtelemetry.StartedAtSourceClaim
		case streamtoken.StartedAtSourceIssuedAt:
			startedSource = streamtelemetry.StartedAtSourceIssuedAt
		}
	}
	if claims.IssuedAt != nil {
		tokenIssuedAt = claims.IssuedAt.Time
		tokenSource = streamtelemetry.TokenIssuedAtSourceVerified
	}
	return
}

// ClientInfoFromRequest captures and normalizes playback client headers at the
// HTTP request boundary.
func ClientInfoFromRequest(r *http.Request) ClientInfo {
	if r == nil {
		return ClientInfo{}
	}
	// Clamped here, at the boundary, rather than only where the session stamps
	// them: the decision logs and playback_route_events are written from this
	// value directly, so a client sending a header-sized build would otherwise
	// reach both despite the published bound. Values stay opaque — trimmed and
	// length-clamped, never parsed or validated against an enum.
	return ClientInfo{
		Name:      r.Header.Get("X-Silo-Client"),
		Version:   r.Header.Get("X-Silo-Client-Version"),
		Build:     r.Header.Get("X-Silo-Client-Build"),
		Channel:   r.Header.Get("X-Silo-Client-Channel"),
		UserAgent: r.UserAgent(),
	}.Normalized()
}
