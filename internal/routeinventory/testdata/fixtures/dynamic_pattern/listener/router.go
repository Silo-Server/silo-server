// Package listener is an analyzer fixture, not shipped code.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter builds a path template at runtime.
func NewRouter(prefix string) http.Handler {
	r := chi.NewRouter()
	r.Get(prefix+"/thing", handler)
	return r
}
