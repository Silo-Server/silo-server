// Package listener is an analyzer fixture, not shipped code. The constructor
// is exported, so any package can build and register on the router.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter() http.Handler {
	return sealedHandler{h: NewInner()}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

// NewInner builds the router.
func NewInner() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
