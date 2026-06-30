package middleware

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// RequireAdminCapability permits a request when the caller is either a normal
// acting admin or has an explicit ACL grant for the requested admin action.
func RequireAdminCapability(authorizer auth.Authorizer, action auth.ACLAction, checkPrimary PrimaryProfileChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeUnauthorized(w, "Authentication required")
				return
			}

			actingAdmin := false
			primaryProfile := false
			if claims.Role == "admin" {
				allowed, err := actingAdminAllowed(r, claims.UserID, checkPrimary)
				if err != nil {
					writeInternalError(w, "Failed to verify active profile")
					return
				}
				if allowed {
					next.ServeHTTP(w, r)
					return
				}
				actingAdmin = allowed
			}

			if checkPrimary == nil {
				primaryProfile = claims.Role == "admin"
			} else if profileID := declaredProfileID(r); profileID != "" {
				isPrimary, found, err := checkPrimary(r.Context(), claims.UserID, profileID)
				if err != nil {
					writeInternalError(w, "Failed to verify active profile")
					return
				}
				primaryProfile = found && isPrimary
			} else if claims.Role == "admin" {
				primaryProfile = true
			}

			if authorizer == nil {
				if claims.Role == "admin" && !actingAdmin {
					writeForbidden(w, "Admin access requires the account's primary profile")
					return
				}
				writeForbidden(w, "Admin capability required")
				return
			}

			decision, err := authorizer.Authorize(r.Context(), auth.AccessRequest{
				UserID:         claims.UserID,
				Action:         action,
				ResourceType:   auth.ResourceServer,
				ResourceID:     "*",
				PrimaryProfile: primaryProfile,
			})
			if err != nil {
				writeInternalError(w, "Failed to verify admin capability")
				return
			}
			if !decision.Allowed {
				if claims.Role == "admin" {
					writeForbidden(w, "Admin access requires the account's primary profile or an explicit access grant")
					return
				}
				writeForbidden(w, "Admin capability required")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
