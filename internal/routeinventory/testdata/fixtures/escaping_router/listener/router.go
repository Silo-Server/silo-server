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
func NewRouter(external mounter) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Get("/visible", handler)
	external.Mount(r)
	return r
}
