// Package root is an analyzer fixture, not shipped code. The mux is built
// without http.NewServeMux(); a zero http.ServeMux is fully functional, so the
// registrations on it are live routes the walk would otherwise never bind.
package root

import "net/http"

func metrics(w http.ResponseWriter, r *http.Request) {}

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
	mux := new(http.ServeMux)
	mux.Handle("/metrics", http.HandlerFunc(metrics))
	return mux
}
