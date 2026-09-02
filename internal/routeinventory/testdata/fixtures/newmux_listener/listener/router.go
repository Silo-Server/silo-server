// Package listener is an analyzer fixture, not shipped code. chi.NewMux is the
// other name for chi.NewRouter; a walk that knew only one of them would drop
// every route on this listener without a word.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point.
func NewRouter() http.Handler {
	r := chi.NewMux()
	r.Get("/api/v1/from-mux", handler)
	return r
}
