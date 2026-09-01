// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point. It returns http.Handler.
func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
