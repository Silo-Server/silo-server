// Package listener is an analyzer fixture, not shipped code. Handing a tracked
// router in as the handler is a mount in disguise: everything behind it would
// hide under one wildcard row.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point.
func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(sub chi.Router) {
		sub.Get("/visible", handler)
		sub.Handle("/loop/*", r)
	})
	return r
}
