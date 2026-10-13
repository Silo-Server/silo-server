package netaccess

import (
	"net/http"
	"net/netip"
)

// IngressTokenHeader carries the per-process ingress token a network access
// provider stamps on every request it proxies to a host listener.
const IngressTokenHeader = "X-Silo-Ingress-Token"

// IngressPeerHeader carries the overlay address of the peer a network access
// provider proxied the request for. It counts only next to a valid ingress
// token.
const IngressPeerHeader = "X-Silo-Ingress-Peer"

// Middleware validates and strips the ingress headers on every request. A
// valid token records the provider's access path on the request context,
// with the peer when the provider named exactly one valid IP; an unknown
// token is rejected with 403 before any handler runs; a request without a
// token stays on the default path. Both headers are removed in all cases so
// they never reach handlers, logs, or upstream proxies, and a peer header a
// client sent on the default path is dropped unread. A nil registry accepts
// no tokens.
func Middleware(registry *Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peers := r.Header.Values(IngressPeerHeader)
			r.Header.Del(IngressPeerHeader)
			values := r.Header.Values(IngressTokenHeader)
			if len(values) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			r.Header.Del(IngressTokenHeader)
			if len(values) != 1 {
				http.Error(w, "invalid ingress token", http.StatusForbidden)
				return
			}
			ingress, ok := registry.Lookup(values[0])
			if !ok {
				http.Error(w, "invalid ingress token", http.StatusForbidden)
				return
			}
			path := Path{Provider: ingress.Provider, InstallationID: ingress.InstallationID, Peer: ingressPeer(peers)}
			next.ServeHTTP(w, r.WithContext(WithPath(r.Context(), path)))
		})
	}
}

// ingressPeer is the single valid IP the provider named, or the zero Addr.
// A malformed or repeated value leaves the request without a peer rather
// than failing it: the overlay path itself is still valid.
func ingressPeer(values []string) netip.Addr {
	if len(values) != 1 {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddr(values[0])
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}
	}
	return addr.Unmap()
}
