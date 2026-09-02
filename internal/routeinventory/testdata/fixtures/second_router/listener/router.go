// Package listener is an analyzer fixture, not shipped code. A second router
// built inside the entry point is not anchored anywhere the walk can see: its
// routes would be recorded at unprefixed paths while the real ones hid behind
// the wildcard it is attached under.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point.
func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/visible", handler)

	sub := chi.NewRouter()
	sub.Get("/inner", handler)
	r.Handle("/prefix/*", sub)
	return r
}
