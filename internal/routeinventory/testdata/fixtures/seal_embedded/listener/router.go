// Package listener is an analyzer fixture, not shipped code. The sealed type
// embeds http.Handler, which is an exported field: h.(sealedHandler).Handler
// is the router.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter() http.Handler {
	return sealedHandler{Handler: newRouter()}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	http.Handler
}

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
