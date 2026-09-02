// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter registers routes from a loop, which would make the set of paths
// depend on runtime data instead of source.
func NewRouter(names []string) http.Handler {
	r := chi.NewRouter()
	for _, name := range names {
		r.Get("/"+name, handler)
	}
	return r
}
