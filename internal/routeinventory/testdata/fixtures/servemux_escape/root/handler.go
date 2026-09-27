// Package root is an analyzer fixture, not shipped code. The mux is handed to a
// helper the walk does not follow, so its registrations would be invisible.
package root

import "net/http"

func hidden(w http.ResponseWriter, r *http.Request) {}

func install(mux *http.ServeMux) {
	mux.Handle("/hidden", http.HandlerFunc(hidden))
}

// newRootHandler is the fixture root listener entry point; it seals the mux.
func newRootHandler() http.Handler {
	return sealedHandler{h: newRootMux()}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRootMux() *http.ServeMux {
	mux := http.NewServeMux()
	install(mux)
	return mux
}
