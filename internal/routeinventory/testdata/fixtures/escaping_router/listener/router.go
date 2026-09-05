// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func handler(w http.ResponseWriter, r *http.Request) {}

type mounter interface {
	Mount(r chi.Router)
}

// NewRouter hands the router to an interface the analyzer cannot follow.
// NewRouter is the fixture listener entry point; it seals the router.
func NewRouter(external mounter) http.Handler {
	return sealedHandler{h: newRouter(external)}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRouter(external mounter) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Get("/visible", handler)
	external.Mount(r)
	return r
}
