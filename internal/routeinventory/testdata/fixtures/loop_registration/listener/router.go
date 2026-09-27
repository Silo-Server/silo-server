// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter registers routes from a loop, which would make the set of paths
// depend on runtime data instead of source.
// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter(names []string) http.Handler {
	return sealedHandler{h: newRouter(names)}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRouter(names []string) chi.Router {
	r := chi.NewRouter()
	for _, name := range names {
		r.Get("/"+name, handler)
	}
	return r
}
