package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// StreamReferenceParam is the query parameter that carries a playback plan's
// signed stream reference on the native delivery routes.
const StreamReferenceParam = "st"

// StreamReferenceVerifier checks a stream reference's signature and expiry
// and returns its claims. The router supplies it, so this package holds no
// signing secret.
type StreamReferenceVerifier func(reference string) (*streamtoken.Claims, error)

// SetStreamReferenceVerifier enables RequireStreamReferenceAuth. Without a
// verifier the native delivery routes accept only the account credential.
func (am *AuthMiddleware) SetStreamReferenceVerifier(verify StreamReferenceVerifier) {
	am.streamReference = verify
}

// RequireStreamReferenceAuth authenticates the native playback delivery
// routes for a receiver that holds only the plan URL and cannot send headers,
// such as a Google Cast device.
//
// A request with an account credential (Authorization header or `token`
// query parameter), or without `st`, runs RequireAuth unchanged. Otherwise
// the reference authorizes the request when it verifies, names a playback
// session rather than a download or theme song, names the session in the
// path, and belongs to an account that still exists and is enabled. The
// request then runs as that account with the current role, and X-Profile-Id
// is rewritten to the reference's profile, so the viewer access that runs
// next re-resolves that profile's limits and skips only the PIN proof the
// plan already required. The handlers then apply the session-deny marker and
// ownership checks they apply to every request.
//
// A reference is not tied to a login session. It lasts as long as
// playback.MaxTokenTTL unless its session is stopped or the account is
// disabled, as on the proxy's token routes. Plans that negotiated
// header-authenticated media carry no reference.
func (am *AuthMiddleware) RequireStreamReferenceAuth(next http.Handler) http.Handler {
	standard := am.RequireAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, credential := extractBearerToken(r); credential || !query.Has(StreamReferenceParam) || am.streamReference == nil || am.apiKeyUserLoader == nil {
			standard.ServeHTTP(w, r)
			return
		}
		reference, err := am.streamReference(query.Get(StreamReferenceParam))
		if err != nil || !reference.AuthorizesPlaybackMedia() {
			writeUnauthorized(w, "Invalid or expired stream reference", ReasonInvalidCredential)
			return
		}
		if reference.SessionID != chi.URLParam(r, "session_id") {
			writeForbidden(w, "The stream reference does not authorize this session")
			return
		}
		user, err := am.apiKeyUserLoader.GetByID(r.Context(), reference.UserID)
		if err != nil && !auth.IsNotFound(err) {
			writeCredentialCheckUnavailable(w, r, err)
			return
		}
		if err != nil || user == nil || user.ID != reference.UserID {
			writeUnauthorized(w, "Invalid or expired stream reference", ReasonInvalidCredential)
			return
		}
		if !user.Enabled {
			writeUnauthorized(w, "User account is disabled", ReasonAccountDisabled)
			return
		}
		claims := &auth.Claims{
			UserID:    user.ID,
			Role:      user.Role,
			ProfileID: reference.ProfileID,
			TokenType: auth.TokenTypeStreamReference,
		}
		if lc := activitylog.GetLogContext(r.Context()); lc != nil {
			uid := claims.UserID
			lc.UserID = &uid
		}
		r = r.Clone(SetClaims(r.Context(), claims))
		if reference.ProfileID != "" {
			r.Header.Set("X-Profile-Id", reference.ProfileID)
		} else {
			r.Header.Del("X-Profile-Id")
		}
		r.Header.Del("X-Profile-Token")
		next.ServeHTTP(w, r)
	})
}
