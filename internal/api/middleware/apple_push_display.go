package middleware

import (
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// RequireApplePushDisplayAuth authenticates the Apple notification display
// endpoint. It accepts the long-lived, profile-scoped display token minted at
// Apple push registration; any other bearer credential is handed to
// `fallback`, which should be the ordinary access-token chain (RequireAuth,
// viewer access, RequireProfile) so normal sessions keep working unchanged.
//
// On the display-token path the middleware validates the token and its
// login session, sets the claims, and rewrites X-Profile-Id to the profile
// in the claims before running `afterAuth`. That should be the same viewer
// access + RequireProfile chain as the fallback: viewer access re-checks the
// profile still exists and belongs to the user (a deleted profile's token
// must stop working) while skipping the PIN proof, which the token already
// represents. The header rewrite means a token cannot be replayed against
// another profile's inbox.
func (am *AuthMiddleware) RequireApplePushDisplayAuth(
	fallback func(http.Handler) http.Handler,
	afterAuth func(http.Handler) http.Handler,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		standard := fallback(next)
		display := next
		if afterAuth != nil {
			display = afterAuth(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Header only. extractBearerToken also accepts ?token= for media
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
			// Same attribution RequireAuth performs, so display fetches are
			// not anonymous in the activity and request logs.
			if lc := activitylog.GetLogContext(r.Context()); lc != nil {
				uid := claims.UserID
				lc.UserID = &uid
				lc.ImpersonatorUserID = claims.ImpersonatorUserID
				lc.SessionID = claims.SessionID
			}
			ctx := SetClaims(r.Context(), claims)
			ctx = SetProfileID(ctx, claims.ProfileID)
			r = r.WithContext(ctx)
			r.Header.Set("X-Profile-Id", claims.ProfileID)
			r.Header.Del("X-Profile-Token")
			display.ServeHTTP(w, r)
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
