// Package middleware provides HTTP middleware for the Silo API,
// including authentication and authorization.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

// claimsKey is the context key for storing JWT claims.
const claimsKey contextKey = "claims"

// SessionValidator checks whether a session is still valid (not revoked/expired).
type SessionValidator interface {
	IsValid(ctx context.Context, sessionID string) (bool, error)
}

// TokenValidator validates a JWT token string and returns the parsed claims.
type TokenValidator interface {
	ValidateToken(tokenStr string) (*auth.Claims, error)
}

// APIKeyValidator looks up an API key by its full key string.
type APIKeyValidator interface {
	GetByKey(ctx context.Context, key string) (*models.APIKey, error)
	UpdateLastUsed(ctx context.Context, id int64) error
}

// APIKeyUserLoader loads a user by ID for API key authentication.
type APIKeyUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// AuthMiddleware provides HTTP middleware for JWT-based authentication with
// session validity caching.
type AuthMiddleware struct {
	tokenValidator   TokenValidator
	sessionValidator SessionValidator
	apiKeyValidator  APIKeyValidator  // nil if API keys not configured
	apiKeyUserLoader APIKeyUserLoader // nil if API keys not configured

	apiKeyLastUsed *auth.APIKeyLastUsedTracker
}

// NewAuthMiddleware creates a new AuthMiddleware with the given token validator
// and session validator.
func NewAuthMiddleware(tv TokenValidator, sv SessionValidator, akv APIKeyValidator, akul APIKeyUserLoader) *AuthMiddleware {
	return &AuthMiddleware{
		tokenValidator:   tv,
		sessionValidator: sv,
		apiKeyValidator:  akv,
		apiKeyUserLoader: akul,
		apiKeyLastUsed:   auth.NewAPIKeyLastUsedTracker(akv, nil),
	}
}

// RequireAuth is an HTTP middleware that enforces JWT authentication.
// It extracts the Bearer token from the Authorization header, validates the
// JWT, checks session validity (with an in-memory cache), and sets the
// parsed claims in the request context for downstream handlers.
func (am *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := extractBearerToken(r)
		if !ok {
			writeUnauthorized(w, "Missing or malformed authorization header")
			return
		}

		var claims *auth.Claims

		if strings.HasPrefix(token, "sa_") {
			// API key authentication.
			if am.apiKeyValidator == nil {
				writeUnauthorized(w, "API key authentication not available")
				return
			}

			apiKey, err := am.apiKeyValidator.GetByKey(r.Context(), token)
			if err != nil {
				writeUnauthorized(w, "Invalid API key")
				return
			}

			user, err := am.apiKeyUserLoader.GetByID(r.Context(), apiKey.UserID)
			if err != nil {
				writeUnauthorized(w, "Invalid API key")
				return
			}

			if !user.Enabled {
				writeUnauthorized(w, "User account is disabled")
				return
			}

			am.apiKeyLastUsed.Touch(apiKey.ID)

			claims = &auth.Claims{
				UserID:    user.ID,
				Role:      user.Role,
				SessionID: "",
				TokenType: auth.TokenTypeAPIKey,
				APIKeyID:  apiKey.ID,
				RateTier:  apiKey.RateTier,
			}
		} else {
			// JWT authentication (existing flow).
			var err error
			claims, err = am.tokenValidator.ValidateToken(token)
			if err != nil {
				writeUnauthorized(w, "Invalid or expired token")
				return
			}
			if claims.TokenType != auth.TokenTypeAccess {
				writeUnauthorized(w, "Invalid or expired token")
				return
			}

			valid, err := am.checkSession(r.Context(), claims.SessionID)
			if err != nil || !valid {
				writeUnauthorized(w, "Session is no longer valid")
				return
			}
		}

		// Populate activity log context if present (set by activitylog middleware upstream)
		if lc := activitylog.GetLogContext(r.Context()); lc != nil {
			uid := claims.UserID
			lc.UserID = &uid
			lc.ImpersonatorUserID = claims.ImpersonatorUserID
			lc.SessionID = claims.SessionID
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin is a standalone HTTP middleware that checks if the authenticated
// user has the "admin" role. It expects RequireAuth to have already placed
// claims in the request context.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required")
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

// RequireActingAdmin enforces the admin role plus the household policy that
// admin powers are only exercised through the account's primary profile.
// When the request declares an active profile (X-Profile-Id) that belongs to
// the admin account but is not the primary profile, the request is refused;
// requests with no declared profile keep working (clients that haven't
// selected a profile yet). With a nil checker it behaves exactly like
// RequireAdmin.
//
// Note this enforces the declared profile, not an authenticated one: all
// profiles on an account share the login session, so this is a policy
// boundary for well-behaved clients, not a defense against the account
// holder themselves.
func RequireActingAdmin(checkPrimary PrimaryProfileChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeUnauthorized(w, "Authentication required")
				return
			}

			if claims.Role != "admin" {
				writeForbidden(w, "Admin access required")
				return
			}

			allowed, err := actingAdminAllowed(r, claims.UserID, checkPrimary)
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
// powers given the profile it declares. Allowed when no checker is
// configured, no profile is declared, or the declared profile is the
// account's primary profile. A declared profile that cannot be resolved to
// one of the caller's profiles fails closed: otherwise a non-primary session
// could regain admin powers by sending a bogus X-Profile-Id.
func actingAdminAllowed(r *http.Request, userID int, checkPrimary PrimaryProfileChecker) (bool, error) {
	if checkPrimary == nil {
		return true, nil
	}
	profileID := declaredProfileID(r)
	if profileID == "" {
		return true, nil
	}
	isPrimary, found, err := checkPrimary(r.Context(), userID, profileID)
	if err != nil {
		return false, err
	}
	return found && isPrimary, nil
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

// checkSession checks whether the session is valid, using the in-memory cache
// first and falling back to the session validator on cache miss.
func (am *AuthMiddleware) checkSession(ctx context.Context, sessionID string) (bool, error) {
	return am.sessionValidator.IsValid(ctx, sessionID)
}

// extractBearerToken extracts a JWT from the request. It checks (in order):
//  1. Authorization: Bearer <token> header
//  2. ?token=<token> query parameter (for native media elements that can't set headers)
func extractBearerToken(r *http.Request) (string, bool) {
	// Try Authorization header first.
	if header := r.Header.Get("Authorization"); header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token, true
			}
		}
	}

	// Fall back to query parameter (used by <video> / <audio> src URLs).
	if token := r.URL.Query().Get("token"); token != "" {
		return token, true
	}

	return "", false
}

// errorResponse is the JSON structure for error responses.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// writeUnauthorized writes a 401 JSON error response.
func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   "unauthorized",
		Message: message,
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
