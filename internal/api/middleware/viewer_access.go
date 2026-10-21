package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
)

// ViewerAccessRetryAfterSeconds is the Retry-After of a request refused
// because the viewer's access policy ran out of time. Evaluations are short,
// so the next one usually fits.
const ViewerAccessRetryAfterSeconds = 1

// ViewerResolver resolves the effective viewer access scope for a request.
type ViewerResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

// ViewerAccessMiddleware resolves and stores viewer access scope in context.
type ViewerAccessMiddleware struct {
	resolver ViewerResolver
}

// NewViewerAccessMiddleware creates a middleware from a scope resolver.
func NewViewerAccessMiddleware(resolver ViewerResolver) *ViewerAccessMiddleware {
	return &ViewerAccessMiddleware{resolver: resolver}
}

// RequireViewerAccess resolves viewer scope from auth + profile headers.
func (m *ViewerAccessMiddleware) RequireViewerAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required", ReasonAuthenticationRequired)
			return
		}

		profileID := r.Header.Get("X-Profile-Id")
		input := access.ResolveInput{
			UserID:       claims.UserID,
			SessionID:    claims.SessionID,
			ProfileID:    profileID,
			ProfileToken: r.Header.Get("X-Profile-Token"),
			// API keys have no PIN proof by design. A display token or a
			// direct-download link was issued to an already verified profile
			// and carries the profile in its claims; the profile still has
			// to exist and be owned by the user, which Resolve checks.
			SkipPINVerification: claims.TokenType == auth.TokenTypeAPIKey ||
				claims.TokenType == auth.TokenTypeApplePushDisplay ||
				claims.TokenType == auth.TokenTypeDirectDownloadLink,
		}

		scope, err := m.resolver.Resolve(r.Context(), input)
		if err != nil {
			switch {
			case errors.Is(err, access.ErrProfileUnverified):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(errorResponse{
					Error:   "profile_unverified",
					Message: "Profile verification required",
				})
				return
			case errors.Is(err, access.ErrProfileNotFound):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(errorResponse{
					Error:   "not_found",
					Message: "Profile not found",
				})
				return
			case errors.Is(err, policy.ErrPolicyEvalTimeout):
				// Still refused, but the policy never decided: answer like a
				// credential that could not be checked, so the client retries
				// instead of treating it as a server fault.
				recordDenialReason(w, ReasonViewerAccessUnavailable)
				w.Header().Set("Retry-After", strconv.Itoa(ViewerAccessRetryAfterSeconds))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(errorResponse{
					Error:   CodeServiceUnavailable,
					Message: "Viewer access could not be resolved right now; try again shortly",
				})
				return
			default:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(errorResponse{
					Error:   "internal_error",
					Message: "Failed to resolve viewer access",
				})
				return
			}
		}

		ctx := access.SetScope(r.Context(), scope)
		if profileID != "" {
			ctx = SetProfileID(ctx, profileID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
