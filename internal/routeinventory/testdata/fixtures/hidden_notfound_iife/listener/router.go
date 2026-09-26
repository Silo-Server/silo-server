// Package listener is an analyzer fixture, not shipped code. The NotFound
// handler is an immediately invoked function that registers a route on the
// captured router before returning the fallback. The route is served; the
// walk never sees it unless the argument is leak-checked.
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
	r.NotFound(func() http.HandlerFunc {
		r.Get("/hidden", handler)
		return http.NotFound
	}())
	return r
}
