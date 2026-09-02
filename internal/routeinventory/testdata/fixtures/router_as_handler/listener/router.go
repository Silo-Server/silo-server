// Package listener is an analyzer fixture, not shipped code. Handing a tracked
// router in as the handler is a mount in disguise: everything behind it would
// hide under one wildcard row.
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
	r.Route("/api/v1", func(sub chi.Router) {
		sub.Get("/visible", handler)
		sub.Handle("/loop/*", r)
	})
	return r
}
