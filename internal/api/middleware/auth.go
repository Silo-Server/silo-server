// Package middleware provides HTTP middleware for the Silo API,
// including authentication and authorization.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/logredact"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

// claimsKey is the context key for storing JWT claims.
const claimsKey contextKey = "claims"

// SessionValidator checks a login session on every access-token request.
// ActiveSessionRole reports whether the session is still active (not revoked
// or expired) and the current role of the account it belongs to, in one
// lookup; auth.SessionRepository implements it. It answers active=false with
// no error for a missing, revoked or expired session; an error means the
// session could not be checked, and the caller must not treat it as ended.
type SessionValidator interface {
	ActiveSessionRole(ctx context.Context, sessionID string) (role string, active bool, err error)
}

// TokenValidator validates a JWT token string and returns the parsed claims.
type TokenValidator interface {
	ValidateToken(tokenStr string) (*auth.Claims, error)
}

// APIKeyValidator looks up an API key by its full key string. An unknown key
// is auth.ErrAPIKeyNotFound; any other error means the key could not be
// checked.
type APIKeyValidator interface {
	GetByKey(ctx context.Context, key string) (*models.APIKey, error)
	UpdateLastUsed(ctx context.Context, id int64) error
}

// APIKeyUserLoader loads a user by ID for API key authentication. A missing
// account is an error matching auth.IsNotFound; any other error means the
// account could not be checked.
type APIKeyUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// AuthMiddleware provides HTTP middleware for JWT and API key authentication.
type AuthMiddleware struct {
	tokenValidator   TokenValidator
	sessionValidator SessionValidator
	apiKeyValidator  APIKeyValidator  // nil if API keys not configured
	apiKeyUserLoader APIKeyUserLoader // nil if API keys not configured

	apiKeyLastUsed  *auth.APIKeyLastUsedTracker
	sessionLastSeen *auth.SessionLastSeenTracker
}

// NewAuthMiddleware creates a new AuthMiddleware with the given token validator
// and session validator.
func NewAuthMiddleware(tv TokenValidator, sv SessionValidator, akv APIKeyValidator, akul APIKeyUserLoader) *AuthMiddleware {
	updater, _ := sv.(auth.SessionLastSeenUpdater)
	return &AuthMiddleware{
		tokenValidator:   tv,
		sessionValidator: sv,
		apiKeyValidator:  akv,
		apiKeyUserLoader: akul,
		apiKeyLastUsed:   auth.NewAPIKeyLastUsedTracker(akv, nil),
		sessionLastSeen:  auth.NewSessionLastSeenTracker(updater, nil),
	}
}

// RequireAuth is an HTTP middleware that enforces JWT authentication.
// It extracts the Bearer token from the Authorization header, validates the
// JWT, checks session validity with the SessionValidator on every request (the
// middleware keeps no cache, so a revocation applies to the session's next
// request), and sets the parsed claims in the request context for downstream
// handlers.
//
// The same lookup returns the account's current role. Admin gates trust the
// role in the access token, so a token minted before an admin changed the
// account's role is refused with ReasonTokenRefreshRequired: the session
// stays valid, and a refresh issues a token carrying the new role.
//
// A lookup that fails (the database is unreachable or times out) judges
// nothing about the credential, so it is answered with a retryable 503
// (writeCredentialCheckUnavailable), never a 401 that tells the client its
// sign-in is gone.
func (am *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := extractBearerToken(r)
		if !ok {
			writeUnauthorized(w, "Missing or malformed authorization header", ReasonAuthenticationRequired)
			return
		}

		var claims *auth.Claims

		if strings.HasPrefix(token, "sa_") {
			// API key authentication.
			if am.apiKeyValidator == nil {
				writeUnauthorized(w, "API key authentication not available", ReasonAuthenticationRequired)
				return
			}

			apiKey, err := am.apiKeyValidator.GetByKey(r.Context(), token)
			if err != nil && !errors.Is(err, auth.ErrAPIKeyNotFound) {
				writeCredentialCheckUnavailable(w, r, err)
				return
			}
			if err != nil || apiKey == nil || apiKey.UserID <= 0 || am.apiKeyUserLoader == nil {
				writeUnauthorized(w, "Invalid API key", ReasonInvalidCredential)
				return
			}

			user, err := am.apiKeyUserLoader.GetByID(r.Context(), apiKey.UserID)
			if err != nil && !auth.IsNotFound(err) {
				writeCredentialCheckUnavailable(w, r, err)
				return
			}
			if err != nil || user == nil || user.ID <= 0 {
				writeUnauthorized(w, "Invalid API key", ReasonInvalidCredential)
				return
			}

			if !user.Enabled {
				writeUnauthorized(w, "User account is disabled", ReasonAccountDisabled)
				return
			}

			if !apiKeyScopesAllow(apiKey.Scopes, r) {
				writeForbidden(w, "API key scopes do not permit this route")
				return
			}

			am.apiKeyLastUsed.Touch(apiKey.ID)

			claims = &auth.Claims{
				UserID:       user.ID,
				Role:         user.Role,
				SessionID:    "",
				TokenType:    auth.TokenTypeAPIKey,
				APIKeyID:     apiKey.ID,
				RateTier:     apiKey.RateTier,
				APIKeyScopes: apiKey.Scopes,
			}
		} else {
			// JWT authentication (existing flow).
			var err error
			claims, err = am.tokenValidator.ValidateToken(token)
			if err != nil {
				writeUnauthorized(w, "Invalid or expired token", ReasonInvalidCredential)
				return
			}
			if claims == nil || claims.UserID <= 0 || claims.TokenType != auth.TokenTypeAccess {
				writeUnauthorized(w, "Invalid or expired token", ReasonInvalidCredential)
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
			if role != claims.Role {
				writeUnauthorized(w, "The account's role changed; refresh the access token", ReasonTokenRefreshRequired)
				return
			}
			am.sessionLastSeen.Touch(claims.SessionID)
		}

		// Populate activity log context if present (set by activitylog middleware upstream)
		if lc := activitylog.GetLogContext(r.Context()); lc != nil {
			uid := claims.UserID
			lc.UserID = &uid
			lc.ImpersonatorUserID = claims.ImpersonatorUserID
			lc.SessionID = claims.SessionID
		}

		// Attributed above, so the audit log shows whose restricted session
		// was refused.
		if claims.PasswordChangeRequired && !passwordChangeRoutes[r.Method+" "+r.URL.Path] {
			writePasswordChangeRequired(w)
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// passwordChangeRoutes are the only routes a session holding a temporary
// password may call: enough to read the account, replace the password, and
// sign out. Refreshing the session afterwards is a public route; the refreshed
// tokens drop the restriction once the account has a new password.
var passwordChangeRoutes = map[string]bool{
	"GET /api/v1/auth/me":                     true,
	"GET /api/v1/auth/account/capability":     true,
	"POST /api/v1/auth/account/password":      true,
	"POST /api/v1/auth/logout":                true,
	"GET /api/v2/account/me":                  true,
	"GET /api/v2/account/password/capability": true,
	"POST /api/v2/account/password":           true,
	"POST /api/v2/auth/logout":                true,
}

// RequireAdmin is a standalone HTTP middleware that checks if the authenticated
// user has the "admin" role. It expects RequireAuth to have already placed
// claims in the request context.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required", ReasonAuthenticationRequired)
			return
		}

		if claims.Role != "admin" {
			writeForbidden(w, "Admin access required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// PrimaryProfileChecker reports whether profileID belongs to userID and, if
// so, whether it is the household primary profile. found must be false when
// the profile does not exist or belongs to a different account.
type PrimaryProfileChecker func(ctx context.Context, userID int, profileID string) (isPrimary bool, found bool, err error)

// HouseholdProfileRequirement reports whether the household on userID's
// account requires a declared profile: some profile on it has a PIN or an
// access limit (access.HouseholdRequiresProfile). It decides whether an admin
// request that declares no profile may exercise admin powers.
type HouseholdProfileRequirement func(ctx context.Context, userID int) (bool, error)

// RequireActingAdmin enforces the admin role plus the household policy that
// admin powers are only exercised through the account's primary profile.
// When the request declares an active profile (X-Profile-Id) that belongs to
// the admin account but is not the primary profile, the request is refused.
// A request with no declared profile keeps working (clients that haven't
// selected a profile yet) only while the household has no PIN-protected or
// access-limited profile; otherwise a device signed into the account could
// regain admin powers, and mint credentials that skip profile PINs, by
// omitting the header. API keys keep their profile-less access. A nil
// checkPrimary disables the declared-profile policy, and a nil
// requiresProfile the profile-less one; with both nil it behaves exactly
// like RequireAdmin.
//
// Note this enforces the declared profile, not an authenticated one: all
// profiles on an account share the login session, so this is a policy
// boundary for well-behaved clients, not a defense against the account
// holder themselves. The viewer gate that runs before it on the admin route
// groups verifies a PIN-locked declared profile's X-Profile-Token.
func RequireActingAdmin(checkPrimary PrimaryProfileChecker, requiresProfile HouseholdProfileRequirement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeUnauthorized(w, "Authentication required", ReasonAuthenticationRequired)
				return
			}

			if claims.Role != "admin" {
				writeForbidden(w, "Admin access required")
				return
			}

			allowed, err := actingAdminAllowed(r, claims, checkPrimary, requiresProfile)
			if err != nil {
				writeInternalError(w, "Failed to verify active profile")
				return
			}
			if !allowed {
				writeForbidden(w, "Admin access requires the account's primary profile")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// actingAdminAllowed reports whether an admin request may exercise admin
// powers given the profile it declares. With no declared profile it is
// allowed unless profileLessAdminRefused says otherwise. With one, it is
// allowed when no checker is configured or the declared profile is the
// account's primary profile. A declared profile that cannot be resolved to
// one of the caller's profiles fails closed: otherwise a non-primary session
// could regain admin powers by sending a bogus X-Profile-Id.
func actingAdminAllowed(r *http.Request, claims *auth.Claims, checkPrimary PrimaryProfileChecker, requiresProfile HouseholdProfileRequirement) (bool, error) {
	profileID := declaredProfileID(r)
	if profileID == "" {
		refused, err := profileLessAdminRefused(r.Context(), claims, requiresProfile)
		return !refused, err
	}
	if checkPrimary == nil {
		return true, nil
	}
	isPrimary, found, err := checkPrimary(r.Context(), claims.UserID, profileID)
	if err != nil {
		return false, err
	}
	return found && isPrimary, nil
}

// profileLessAdminRefused reports whether an admin request that declares no
// profile must be refused admin powers: its household has a PIN-protected or
// access-limited profile. API keys are exempt (a key is an account credential
// that skips profile PINs by design, bounded by its own scopes), and a nil
// requirement disables the check. A lookup error is returned so the caller
// fails closed.
func profileLessAdminRefused(ctx context.Context, claims *auth.Claims, requiresProfile HouseholdProfileRequirement) (bool, error) {
	if requiresProfile == nil || claims == nil || claims.TokenType == auth.TokenTypeAPIKey {
		return false, nil
	}
	return requiresProfile(ctx, claims.UserID)
}

// declaredProfileID returns the active profile the request declares: the
// profile context when RequireProfile ran earlier in the chain, otherwise
// the raw X-Profile-Id header.
func declaredProfileID(r *http.Request) string {
	if id := GetProfileID(r.Context()); id != "" {
		return id
	}
	return r.Header.Get("X-Profile-Id")
}

// SetClaims stores JWT claims in the context. This is useful for testing
// handlers that depend on authentication without going through the full
// middleware chain.
func SetClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// GetClaims retrieves the JWT claims from the context. Returns nil if no
// claims are present (caller should handle this case).
func GetClaims(ctx context.Context) *auth.Claims {
	claims, ok := ctx.Value(claimsKey).(*auth.Claims)
	if !ok {
		return nil
	}
	return claims
}

// IsAdmin reports whether the context's authenticated user account has the
// admin role. Returns false when no claims are present. Note this is the
// account-level role; it says nothing about which household profile is active.
func IsAdmin(ctx context.Context) bool {
	claims := GetClaims(ctx)
	return claims != nil && claims.Role == "admin"
}

// GetUserID retrieves the user ID from the JWT claims in the context.
// Returns 0 if no claims are present.
func GetUserID(ctx context.Context) int {
	claims := GetClaims(ctx)
	if claims == nil {
		return 0
	}
	return claims.UserID
}

// extractBearerToken extracts a JWT from the request. It checks (in order):
//  1. Authorization: Bearer <token> header
//  2. ?token=<token> query parameter (for native media elements that can't set headers)
func extractBearerToken(r *http.Request) (string, bool) {
	if token, ok := parseBearerHeader(r.Header.Get("Authorization")); ok {
		return token, true
	}

	// Fall back to query parameter (used by <video> / <audio> src URLs).
	if token := r.URL.Query().Get("token"); token != "" {
		return token, true
	}

	return "", false
}

// parseBearerHeader parses only the captured Authorization header, without
// accepting the media URL query-credential fallback.
func parseBearerHeader(header string) (string, bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		if token := strings.TrimSpace(parts[1]); token != "" {
			return token, true
		}
	}
	return "", false
}

// errorResponse is the JSON structure for error responses.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// DenialReasonRecorder is implemented by a ResponseWriter that wants the
// machine-readable reason behind a denial as well as the JSON body. The v1
// wire response is unchanged — the reason is never written to it, because the
// ratified v1 error body is exactly {"error","message"} — so this is purely
// additive. internal/apiv2 wraps its gate chain in such a writer and
// translates the reason into the matching Problem Details type.
type DenialReasonRecorder interface {
	RecordDenialReason(reason string)
}

// recordDenialReason hands the reason to a writer that asked for it, and does
// nothing for every other writer.
func recordDenialReason(w http.ResponseWriter, reason string) {
	if reason == "" {
		return
	}
	if rec, ok := w.(DenialReasonRecorder); ok {
		rec.RecordDenialReason(reason)
	}
}

// Denial reasons. They refine an error code that covers denials a caller must
// tell apart (every 401 is "unauthorized"; two different gates write
// "bad_request"). Add, never rename: internal/apiv2 switches on these, and
// TestDenialCodesAreStable pins them.
const (
	// ReasonAuthenticationRequired: no usable credential was presented.
	ReasonAuthenticationRequired = "authentication_required"
	// ReasonInvalidCredential: a credential was presented and rejected.
	ReasonInvalidCredential = "invalid_credential"
	// ReasonAccountDisabled: the credential resolves to a disabled account.
	ReasonAccountDisabled = "account_disabled"
	// ReasonSessionInvalid: the credential is well-formed but its login
	// session no longer exists.
	ReasonSessionInvalid = "session_invalid"
	// ReasonTokenRefreshRequired: the login session is valid, but the access
	// token was minted before the account's role changed. Refreshing the
	// session issues a token with the current role; the client must not sign
	// out.
	ReasonTokenRefreshRequired = "token_refresh_required"
	// ReasonProfileHeaderRequired: RequireProfile found no X-Profile-Id.
	ReasonProfileHeaderRequired = "profile_header_required"
	// ReasonItemIDRequired: an item-scoped permission gate found no {id} path
	// parameter on the route it was mounted on.
	ReasonItemIDRequired = "item_id_required"
	// ReasonViewerAccessUnavailable: the viewer's access policy ran out of
	// time, so the request was refused without a decision (a 503
	// service_unavailable, like a credential that could not be checked).
	ReasonViewerAccessUnavailable = "viewer_access_unavailable"
)

// writeUnauthorized writes a 401 JSON error response. reason is one of the
// Reason* constants and names which 401 this is.
func writeUnauthorized(w http.ResponseWriter, message, reason string) {
	recordDenialReason(w, reason)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   "unauthorized",
		Message: message,
	})
}

// CodePasswordChangeRequired is the error code of a request a session made
// before replacing its temporary password. internal/apiv2 renders it as the
// password_change_required problem type.
const CodePasswordChangeRequired = "password_change_required"

// writePasswordChangeRequired writes the 403 a restricted session gets for
// any route outside passwordChangeRoutes.
func writePasswordChangeRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   CodePasswordChangeRequired,
		Message: "Choose a new password to continue",
	})
}

// CodeServiceUnavailable is the error code of a request whose credential
// could not be checked because the store holding login sessions, API keys or
// accounts did not answer. The credential was not judged: the client keeps
// its session and retries after Retry-After. internal/apiv2 renders it as the
// dependency_unavailable problem type. Add, never rename.
const CodeServiceUnavailable = "service_unavailable"

// CredentialCheckRetryAfterSeconds is the Retry-After a failed credential
// check carries (auth.SessionCheckRetryAfterSeconds).
const CredentialCheckRetryAfterSeconds = auth.SessionCheckRetryAfterSeconds

// writeCredentialCheckUnavailable writes the 503 for a credential check that
// failed in the store rather than refusing the credential.
func writeCredentialCheckUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() == nil {
		slog.WarnContext(r.Context(), "credential check failed; answering 503", "component", "auth", "error", logredact.SanitizeText(err.Error()))
	}
	w.Header().Set("Retry-After", strconv.Itoa(CredentialCheckRetryAfterSeconds))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   CodeServiceUnavailable,
		Message: "Sign-in could not be checked right now; try again shortly",
	})
}

// writeInternalError writes a 500 JSON error response.
func writeInternalError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   "internal_error",
		Message: message,
	})
}

// writeForbidden writes a 403 JSON error response.
func writeForbidden(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   "forbidden",
		Message: message,
	})
}
