package middleware

import "net/http"

// StreamDeliveryCORS sets the cross-origin headers required by Cast receivers
// before any authentication, rate-limit, viewer-access, or handler response.
func StreamDeliveryCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Range")
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")
		next.ServeHTTP(w, r)
	})
}
