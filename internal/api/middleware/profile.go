package middleware

import (
	"context"
	"encoding/json"
	"net/http"
)

// profileKey is the context key for storing the profile ID.
const profileKey contextKey = "profile_id"

// RequireProfile is an HTTP middleware that reads the X-Profile-Id header
// and stores it in the request context. Returns 400 if the header is missing
// or empty.
func RequireProfile(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profileID := r.Header.Get("X-Profile-Id")
		if profileID == "" {
			recordDenialReason(w, ReasonProfileHeaderRequired)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{
				Error:   "bad_request",
				Message: "X-Profile-Id header is required",
			})
			return
		}

		ctx := context.WithValue(r.Context(), profileKey, profileID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetProfileID retrieves the profile ID from the context. Returns an empty
// string if no profile ID is present.
func GetProfileID(ctx context.Context) string {
	id, _ := ctx.Value(profileKey).(string)
	return id
}

// SetProfileID stores a profile ID in the context. This is useful for
// testing handlers that depend on a profile without going through the
// full middleware chain.
func SetProfileID(ctx context.Context, profileID string) context.Context {
	return context.WithValue(ctx, profileKey, profileID)
}
