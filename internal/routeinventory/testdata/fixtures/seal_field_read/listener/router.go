// Package listener is an analyzer fixture, not shipped code. A helper in the
// listener package reads the sealed field and registers on it.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter() http.Handler {
	return sealedHandler{h: newRouter()}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func unseal(h http.Handler) {
	if s, ok := h.(sealedHandler); ok {
		s.h.(chi.Router).Get("/hidden", handler)
	}
}

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
