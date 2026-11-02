package auth

import "github.com/Silo-Server/silo-server/internal/models"

// remotePlaybackClaims narrows the claims of new tokens for a
// remote-playback handoff session, as SessionAuthority.Narrow does for the
// tokens it already holds, and caps their expiry at the session's absolute
// end. See docs/architecture/device-login.md#remote-playback-handoff.
func remotePlaybackClaims(claims Claims, session *models.AuthSession) Claims {
	SessionAuthority{Purpose: session.Purpose, BoundProfileID: session.BoundProfileID}.Narrow(&claims)
	if session.AbsoluteExpiresAt != nil {
		claims.SessionEndsAt = *session.AbsoluteExpiresAt
	}
	return claims
}
