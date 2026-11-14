package httpstream

import "net/http"

// NoSniff sets X-Content-Type-Options: nosniff on every response of a
// listener, so a browser loads a response as a script or a stylesheet only
// when its Content-Type says it is one.
//
// The web app's Content-Security-Policy allows scripts from its own origin
// (script-src 'self'), and API routes return bytes that users and libraries
// supply. Without this header a browser runs such a response as a script
// when a page names it by URL, whatever Content-Type it carries. Mount it
// first, so error responses carry it too.
func NoSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
