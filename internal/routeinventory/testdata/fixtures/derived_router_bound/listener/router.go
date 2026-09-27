// Package listener is an analyzer fixture, not shipped code. A router derived
// from the listener's router by With() is bound to a name and registered on;
// the walk models With() only inline, so the binding is refused.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

func mw(next http.Handler) http.Handler { return next }

// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter() http.Handler {
	return sealedHandler{h: newRouter()}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)

	alt := r.With(mw)
	alt.Get("/hidden", handler)
	return r
}
