// Package other is an analyzer fixture, not shipped code. It stands in for a
// fourth listener nobody declared.
package other

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// Handler builds an undeclared listener.
func Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/undeclared", handler)
	return r
}
