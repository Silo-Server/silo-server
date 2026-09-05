// Package listener is an analyzer fixture, not shipped code. The second router
// is bound with `var` rather than `:=`. A walk that recognized only the short
// form would bind nothing here, walk none of the registrations on it, and hide
// /inner behind the wildcard the router is attached under.
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
	r := chi.NewRouter()
	r.Get("/visible", handler)

	var sub = chi.NewRouter()
	sub.Get("/inner", handler)
	r.Handle("/prefix/*", sub)
	return r
}
