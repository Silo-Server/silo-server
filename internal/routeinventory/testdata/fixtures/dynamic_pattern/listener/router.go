// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter builds a path template at runtime.
// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter(prefix string) http.Handler {
	return sealedHandler{h: newRouter(prefix)}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRouter(prefix string) chi.Router {
	r := chi.NewRouter()
	r.Get(prefix+"/thing", handler)
	return r
}
