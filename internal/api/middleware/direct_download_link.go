package middleware

import (
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// DirectDownloadLinkParam is the query parameter that carries a
// direct-download link token (auth.TokenTypeDirectDownloadLink).
const DirectDownloadLinkParam = "dl"

// RequireDirectDownloadAuth authenticates the direct-download byte routes. A
// request carrying the `dl` query parameter is authenticated by that link
// token alone; every other request runs RequireAuth unchanged, so header and
// `?token=` callers keep their current behavior.
//
// A link token is minted by a profile-scoped request that already passed
// viewer access, including the profile's PIN. It names one file, one profile
// and a login session. The middleware refuses a link for a different
// `file_id`, checks that the session is still active, sets the claims, and
// rewrites X-Profile-Id to the profile in the token (dropping any
// X-Profile-Token). The viewer access that runs next re-resolves that
// profile's limits, skipping only the PIN proof the link represents, so a
// link minted by a child profile is held to the child's limits.
func (am *AuthMiddleware) RequireDirectDownloadAuth(next http.Handler) http.Handler {
	standard := am.RequireAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if !query.Has(DirectDownloadLinkParam) {
			standard.ServeHTTP(w, r)
			return
		}
		claims, err := am.tokenValidator.ValidateToken(query.Get(DirectDownloadLinkParam))
		if err != nil || claims == nil || claims.TokenType != auth.TokenTypeDirectDownloadLink ||
			claims.UserID <= 0 || claims.ProfileID == "" || claims.FileID <= 0 {
			writeUnauthorized(w, "Invalid or expired download link", ReasonInvalidCredential)
			return
		}
		if strconv.Itoa(claims.FileID) != query.Get("file_id") {
			writeForbidden(w, "The download link does not authorize this file")
			return
		}
		role, active, err := am.sessionValidator.ActiveSessionRole(r.Context(), claims.SessionID)
		if err != nil {
			writeCredentialCheckUnavailable(w, r, err)
			return
		}
		if !active {
			writeUnauthorized(w, "Session is no longer valid", ReasonSessionInvalid)
			return
		}
		// A link lasts minutes and grants nothing role-gated, so a role
		// change does not refuse it; the request carries the current role.
		claims.Role = role
		if lc := activitylog.GetLogContext(r.Context()); lc != nil {
			uid := claims.UserID
			lc.UserID = &uid
			lc.ImpersonatorUserID = claims.ImpersonatorUserID
			lc.SessionID = claims.SessionID
		}
		r = r.Clone(SetClaims(r.Context(), claims))
		r.Header.Set("X-Profile-Id", claims.ProfileID)
		r.Header.Del("X-Profile-Token")
		next.ServeHTTP(w, r)
	})
}
