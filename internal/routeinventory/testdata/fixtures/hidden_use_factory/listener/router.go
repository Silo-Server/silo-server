// Package listener is an analyzer fixture, not shipped code. The router is
// handed to a middleware factory through a structural interface that spells
// one registration method; the factory registers on it.
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

type registrar interface {
	Get(string, http.HandlerFunc)
}

func factory(reg registrar) func(http.Handler) http.Handler {
	reg.Get("/hidden", handler)
	return func(next http.Handler) http.Handler { return next }
}

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(factory(r))
	r.Get("/visible", handler)
	return r
}
