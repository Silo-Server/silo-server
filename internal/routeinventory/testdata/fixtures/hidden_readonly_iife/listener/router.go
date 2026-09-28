// Package listener is an analyzer fixture, not shipped code. A read-only
// router method is called with an argument that registers on the captured
// router while it is evaluated.
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
	r.Match(chi.NewRouteContext(), func() string {
		r.Get("/hidden", handler)
		return "GET"
	}(), "/visible")
	return r
}
