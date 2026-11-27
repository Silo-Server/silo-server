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

// StreamReferenceAttempt is the playback attempt a reference names: the
// account and profile that own it, the login session that started it (empty
// when an API key did), and whether it is still live (not stopped and not
// expired).
type StreamReferenceAttempt struct {
	UserID         int
	ProfileID      string
	LoginSessionID string
	Live           bool
}

// StreamReferenceSession looks up the playback attempt for a session ID. A
// missing attempt is a zero attempt with no error; an error means the attempt
// could not be checked.
type StreamReferenceSession func(ctx context.Context, sessionID string) (StreamReferenceAttempt, error)

// LoginSessionID returns the login session of the request's access token, or
// "" for an API key or any other credential. A playback attempt records it so
// the plan URL's stream reference ends when that session does.
func LoginSessionID(ctx context.Context) string {
	if claims := GetClaims(ctx); claims != nil && claims.TokenType == auth.TokenTypeAccess {
		return claims.SessionID
	}
	return ""
}

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
// path, and that session's playback attempt is still live, owned by the
// reference's account and profile, and was started by a login session that
// is still active. The request then runs as that account with its current
// role, and X-Profile-Id is rewritten to the reference's profile, so the
// viewer access that runs next re-resolves that profile's limits and skips
// only the PIN proof the plan already required. The handlers then apply the
// session-deny marker and ownership checks they apply to every request.
//
// A reference therefore ends when its playback session stops or expires, even
// where no session-deny marker store is configured, and when the login
// session that started it signs out or is revoked (disabling the account
// revokes it). An attempt with no login session, started with an API key or
// before attempts recorded one, falls back to RequireAuth. Plans that
// negotiated header-authenticated media carry no reference.
func (am *AuthMiddleware) RequireStreamReferenceAuth(next http.Handler) http.Handler {
	standard := am.RequireAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if _, credential := extractBearerToken(r); credential || !query.Has(StreamReferenceParam) || am.streamReference == nil || am.streamReferenceSession == nil || am.sessionValidator == nil {
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
		attempt, err := am.streamReferenceSession(r.Context(), reference.SessionID)
		if err != nil {
			writeCredentialCheckUnavailable(w, r, err)
			return
		}
		if !attempt.Live {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: CodePlaybackSessionEnded, Message: "The playback session has ended"})
			return
		}
		if attempt.UserID != reference.UserID || attempt.ProfileID != reference.ProfileID {
			writeForbidden(w, "The stream reference does not authorize this session")
			return
		}
		if attempt.LoginSessionID == "" {
			// Not bound to a login session: the account credential is required.
			standard.ServeHTTP(w, r)
			return
		}
		authority, active, err := am.sessionValidator.ActiveSession(r.Context(), attempt.LoginSessionID, reference.UserID)
		if err != nil {
			writeCredentialCheckUnavailable(w, r, err)
			return
		}
		if !active {
			writeUnauthorized(w, "The login session that started this playback has ended", ReasonSessionInvalid)
			return
		}
		// SessionID stays empty: these claims stand for one playback session's
		// media, never for the login session itself.
		claims := &auth.Claims{
			UserID:    reference.UserID,
			Role:      authority.Role,
			ProfileID: reference.ProfileID,
			TokenType: auth.TokenTypeStreamReference,
		}
		if lc := activitylog.GetLogContext(r.Context()); lc != nil {
			uid := claims.UserID
			lc.UserID = &uid
			lc.SessionID = attempt.LoginSessionID
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
