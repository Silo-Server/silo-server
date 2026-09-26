// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point. Returning chi.Router lets a
// caller keep registering after the walk is over.
func NewRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
