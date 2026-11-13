package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// StreamReferenceParam is the query parameter that carries a playback plan's
// signed stream reference on the native delivery routes.
const StreamReferenceParam = "st"

// CodePlaybackSessionEnded refuses a stream reference whose playback session
// was stopped or has expired, with the 410 the delivery handlers give a
// stopped session.
const CodePlaybackSessionEnded = "playback_session_ended"

// StreamReferenceVerifier checks a stream reference's signature and expiry
// and returns its claims. The router supplies it, so this package holds no
// signing secret.
type StreamReferenceVerifier func(reference string) (*streamtoken.Claims, error)

// StreamReferenceSession reports the playback attempt a reference names: the
// account and profile that own it, and whether it is still live (not stopped
// and not expired). A missing attempt is live=false with no error; an error
// means the attempt could not be checked.
type StreamReferenceSession func(ctx context.Context, sessionID string) (userID int, profileID string, live bool, err error)

// SetStreamReferenceVerifier enables RequireStreamReferenceAuth. Without both
// functions the native delivery routes accept only the account credential.
func (am *AuthMiddleware) SetStreamReferenceVerifier(verify StreamReferenceVerifier, session StreamReferenceSession) {
	am.streamReference, am.streamReferenceSession = verify, session
}

// RequireStreamReferenceAuth authenticates the native playback delivery
// routes for a receiver that holds only the plan URL and cannot send headers,
// such as a Google Cast device.
//
// A request with an account credential (Authorization header or `token`
// query parameter), or without `st`, runs RequireAuth unchanged. Otherwise
// the reference authorizes the request when it verifies, names a playback
// session rather than a download or theme song, names the session in the
// path, that session's playback attempt is still live and owned by the
// reference's account and profile, and the account still exists and is
// enabled. The attempt check is what ends a reference when its session is
// stopped, even where no session-deny marker store is configured. The
// request then runs as that account with the current role, and X-Profile-Id
// is rewritten to the reference's profile, so the viewer access that runs
// next re-resolves that profile's limits and skips only the PIN proof the
// plan already required. The handlers then apply the session-deny marker and
// ownership checks they apply to every request.
//
// A reference is not tied to a login session. It lasts as long as
// playback.MaxTokenTTL unless its session is stopped or the account is
// disabled. Plans that negotiated header-authenticated media carry no
// reference.
func (am *AuthMiddleware) RequireStreamReferenceAuth(next http.Handler) http.Handler {
	standard := am.RequireAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, credential := extractBearerToken(r); credential || !query.Has(StreamReferenceParam) || am.streamReference == nil || am.streamReferenceSession == nil || am.apiKeyUserLoader == nil {
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
		owner, ownerProfile, live, err := am.streamReferenceSession(r.Context(), reference.SessionID)
		if err != nil {
			writeCredentialCheckUnavailable(w, r, err)
			return
		}
		if !live {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: CodePlaybackSessionEnded, Message: "The playback session has ended"})
			return
		}
		if owner != reference.UserID || ownerProfile != reference.ProfileID {
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
