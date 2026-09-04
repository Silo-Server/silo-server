package middleware

import (
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// RequireApplePushDisplayAuth authenticates the Apple notification display
// endpoint. It accepts the long-lived, profile-scoped display token minted at
// Apple push registration; any other bearer credential is handed to
// `fallback`, which should be the ordinary access-token chain (RequireAuth,
// viewer access, RequireProfile) so normal sessions keep working unchanged.
//
// The display token path deliberately bypasses viewer-access PIN checks: the
// token was issued to an already-verified profile session, carries that
// profile in its claims, and only unlocks compact notification text. The
// profile in the claims is authoritative; the X-Profile-Id header is ignored
// so a token cannot be replayed against another profile's inbox.
func (am *AuthMiddleware) RequireApplePushDisplayAuth(fallback func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		standard := fallback(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Header only. extractBearerToken also honours ?token= for media
			// elements, but a credential that lives as long as a refresh
			// token must not end up in proxy and access logs via the URL.
			token, ok := headerBearerToken(r)
			if !ok || strings.HasPrefix(token, "sa_") {
				standard.ServeHTTP(w, r)
				return
			}
			claims, err := am.tokenValidator.ValidateToken(token)
			if err != nil || claims.TokenType != auth.TokenTypeApplePushDisplay {
				// Not a display token (or an expired one): let the standard
				// chain produce its usual 401 or succeed on an access token.
				standard.ServeHTTP(w, r)
				return
			}
			if claims.ProfileID == "" {
				writeUnauthorized(w, "Invalid or expired token")
				return
			}
			valid, err := am.checkSession(r.Context(), claims.SessionID)
			if err != nil || !valid {
				writeUnauthorized(w, "Session is no longer valid")
				return
			}
			ctx := SetClaims(r.Context(), claims)
			ctx = SetProfileID(ctx, claims.ProfileID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// headerBearerToken reads only the Authorization header, ignoring the
// ?token= query fallback that extractBearerToken allows.
func headerBearerToken(r *http.Request) (string, bool) {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	return token, token != ""
}
