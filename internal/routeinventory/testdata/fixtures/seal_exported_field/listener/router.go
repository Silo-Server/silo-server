// Package listener is an analyzer fixture, not shipped code. The sealed
// type's field is exported, so reflect can read the router out of it.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter() http.Handler {
	return sealedHandler{H: newRouter()}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	H http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.H.ServeHTTP(w, r) }

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
