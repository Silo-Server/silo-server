// Package listener is an analyzer fixture, not shipped code. The entry
// function returns the router as an http.Handler without sealing it: a caller
// can assert it back and register after the walk is over.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point.
func NewRouter() http.Handler {
	return newRouter()
}

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
