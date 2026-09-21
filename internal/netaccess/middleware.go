package netaccess

import "net/http"

// IngressTokenHeader carries the per-process ingress token a network access
// provider stamps on every request it proxies to a host listener.
const IngressTokenHeader = "X-Silo-Ingress-Token"

// Middleware validates and strips the ingress token header on every request.
// A valid token records the provider's access path on the request context;
// an unknown token is rejected with 403 before any handler runs; a request
// without the header stays on the default path. The header is removed in all
// cases so it never reaches handlers, logs, or upstream proxies. A nil
// registry accepts no tokens.
func Middleware(registry *Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			next.ServeHTTP(w, r.WithContext(WithPath(r.Context(), Path{Provider: ingress.Provider})))
		})
	}
}
