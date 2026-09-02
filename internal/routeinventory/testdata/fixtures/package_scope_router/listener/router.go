// Package listener is an analyzer fixture, not shipped code. The second router
// is built at package scope, so it belongs to no function the walk enters and
// no entry-point allowance can cover it.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

var debugRouter = chi.NewRouter()

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
	r.Handle("/debug/*", debugRouter)
	return r
}
