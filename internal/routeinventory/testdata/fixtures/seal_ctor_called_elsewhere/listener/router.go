// Package listener is an analyzer fixture, not shipped code. A second
// function in the package calls the constructor and hands the router out.
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

// Debug returns an unsealed router.
func Debug() http.Handler {
	r := newRouter()
	r.Get("/hidden", handler)
	return r
}

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
