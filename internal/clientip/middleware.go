package clientip

import (
	"context"
	"net/http"
)

type contextKey string

const clientIPKey contextKey = "client_ip"

// peerIPKey holds the transport-level peer address exactly as the listener saw
// it, before Middleware overwrote RemoteAddr with the resolved client IP.
const peerIPKey contextKey = "peer_addr"

// Middleware returns chi-compatible middleware that resolves the client IP
// and stores it in the request context. It also overwrites r.RemoteAddr so
// that downstream middleware (e.g. chi's Logger) and any code reading
// RemoteAddr directly sees the real client IP instead of the proxy address.
//
// Because the overwrite is destructive, the original transport peer address is
// preserved in the context as well. Anything that must key on an address a
// client cannot forge — rate limiters, abuse controls — has to read
// PeerFromContext rather than RemoteAddr, since a resolved IP is only as
// trustworthy as the forwarding headers it came from.
func Middleware(resolver *Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := r.RemoteAddr
			ip := resolver.ClientIP(r)
			r.RemoteAddr = ip
			ctx := context.WithValue(r.Context(), clientIPKey, ip)
			ctx = context.WithValue(ctx, peerIPKey, peer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromContext retrieves the resolved client IP from the request context.
// Returns empty string if not set.
func FromContext(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey).(string)
	return ip
}

// PeerFromContext returns the transport peer address (host:port, as the
// listener reported it) captured before Middleware overwrote RemoteAddr.
// Returns empty string when the middleware did not run, in which case
// r.RemoteAddr is still the unmodified peer address.
func PeerFromContext(ctx context.Context) string {
	addr, _ := ctx.Value(peerIPKey).(string)
	return addr
}

// SetPeerContext stores a transport peer address in the context. Useful for
// testing handlers that depend on the clientip middleware without going
// through the full chain.
func SetPeerContext(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, peerIPKey, addr)
}

// SetContext stores a client IP in the context. Useful for testing handlers
// that depend on the clientip middleware without going through the full chain.
func SetContext(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}
