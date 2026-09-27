// Package root is an analyzer fixture, not shipped code. A method-aware
// ServeMux pattern means more than the one method it spells: net/http answers
// HEAD on a GET pattern and returns 405 rather than 404 for the rest. The
// inventory does not model that, so it refuses the pattern instead of emitting
// the single row the text reads like.
package root

import "net/http"

func health(w http.ResponseWriter, r *http.Request) {}

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
	mux.HandleFunc("GET /health", health)
	return mux
}
