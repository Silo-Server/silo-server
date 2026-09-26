// Package listener is an analyzer fixture, not shipped code. The inline
// middleware passed to With is an immediately invoked function that registers
// on the captured router.
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

func mw(next http.Handler) http.Handler { return next }

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.With(func() func(http.Handler) http.Handler {
		r.Get("/hidden", handler)
		return mw
	}()).Get("/visible", handler)
	return r
}
