package auth

import "github.com/Silo-Server/silo-server/internal/models"

// boundSessionClaims narrows the claims of new tokens for a bound session (a
// remote-playback handoff or a Watch Party visitor), as
// SessionAuthority.Narrow does for the tokens it already holds, and caps
// their expiry at the session's absolute end. See
// docs/architecture/device-login.md#remote-playback-handoff and
// docs/architecture/watch-party-visitors.md.
func boundSessionClaims(claims Claims, session *models.AuthSession) Claims {
	SessionAuthority{Purpose: session.Purpose, BoundProfileID: session.BoundProfileID, RoomID: session.BoundRoomID, VisitorID: session.BoundVisitorID}.Narrow(&claims)
	if session.AbsoluteExpiresAt != nil {
		claims.SessionEndsAt = *session.AbsoluteExpiresAt
	}
	return claims
}
