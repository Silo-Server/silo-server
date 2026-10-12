package jellycompat

import (
	"fmt"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// compatLoginRateLimitBucket is the native sign-in budget. Jellyfin password
// sign-in spends it too, so password guessing from one client IP is capped
// across both listeners rather than once per listener.
const compatLoginRateLimitBucket = "login"

// compatRateLimit returns the native authenticated-route limiter with a
// Jellyfin-style error body. It spends the global and per-IP budgets; the
// per-API-key tier budget needs native API-key claims, which compat auth does
// not set. A nil middleware, used when rate limiting is disabled, leaves routes
// unlimited.
func compatRateLimit(mw *ratelimit.Middleware) func(http.Handler) http.Handler {
	if mw == nil {
		return passThrough
	}
	return mw.HandlerWithRejectBody(writeCompatRateLimited)
}

// compatAuthEndpointRateLimit returns the native per-endpoint credential
// limiter for bucket, refusing with writeCompatSignInRateLimited.
func compatAuthEndpointRateLimit(mw *ratelimit.Middleware, bucket string) func(http.Handler) http.Handler {
	if mw == nil {
		return passThrough
	}
	return mw.AuthEndpointHandlerWithRejectBody(bucket, writeCompatSignInRateLimited)
}

func passThrough(next http.Handler) http.Handler { return next }

// writeCompatRateLimited writes the 429 in the listener's usual error envelope,
// the same one TooManyStreams uses. Retry-After is already set.
func writeCompatRateLimited(w http.ResponseWriter, retryAfterSeconds int) {
	writeError(w, http.StatusTooManyRequests, "TooManyRequests",
		fmt.Sprintf("Too many requests. Please retry after %d seconds.", retryAfterSeconds))
}

// writeCompatSignInRateLimited is writeCompatRateLimited with status 403, the
// status Jellyfin gives a locked-out account: Jellyfin Web shows a 403 sign-in
// failure as "not authorized at this time" but a 429 as an unreachable server.
// Any of the credential limiter's budgets can refuse, so the message names no
// cause. Retry-After is already set.
func writeCompatSignInRateLimited(w http.ResponseWriter, retryAfterSeconds int) {
	writeError(w, http.StatusForbidden, "TooManyRequests",
		fmt.Sprintf("Too many requests. Please retry after %d seconds.", retryAfterSeconds))
}
