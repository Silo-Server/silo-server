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

// ReportedSessions implements streamtelemetry.ReportedSessionSource.
//
// It is what makes the merged telemetry view complete: without it the view holds
// only sessions that moved bytes through an observed route family, and every
// consumer had to reconcile that set against the legacy store at read time. With
// it, a session a client claims but nothing is delivering for is simply a row in
// the one view carrying Reported with no bytes.
//
// This reports CLAIMS, never measurements. No byte count and no client address
// crosses this boundary: viewer bytes and viewer IP belong exclusively to the
// outermost viewer edge (§2.5), and the session manager only knows what the
// client told it. Session.ClientIP in particular is a resolved request address
// held for display, not evidence that anything was delivered to it.
func (m *SessionManager) ReportedSessions() []streamtelemetry.ReportedSession {
	if m == nil {
		return nil
	}
	sessions := m.AllSessions()
	reported := make([]streamtelemetry.ReportedSession, 0, len(sessions))
	for _, session := range sessions {
		if session == nil || session.ID == "" {
			continue
		}
		reported = append(reported, streamtelemetry.ReportedSession{
			SessionID:   session.ID,
			Subject:     streamtelemetry.UserSubject(session.UserID),
			ProfileID:   session.ProfileID,
			MediaFileID: session.MediaFileID,
			PlayMethod:  string(session.PlayMethod),
			StartedAt:   session.StartedAt,
			Paused:      session.IsPaused,
			// Position is the client's own report. It is the field that made the
			// legacy store over-report liveness in the first place (#666), so it
			// travels as reported state and is never treated as activity.
			PositionSeconds: session.Position,
			RealtimeAlive:   session.HasRealtimeConnection,
		})
	}
	return reported
}
