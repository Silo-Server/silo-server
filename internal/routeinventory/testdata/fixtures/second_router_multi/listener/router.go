// Package listener is an analyzer fixture, not shipped code. Both routers are
// bound by one multi-value assignment. A walk that only matched a single-name
// assignment bound neither of them, so it enumerated no routes at all and said
// nothing about it.
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

func newRouter() chi.Router {
	r, sub := chi.NewRouter(), chi.NewRouter()
	r.Get("/visible", handler)
	sub.Get("/inner", handler)
	r.Handle("/prefix/*", sub)
	return r
}
