// Package other is an analyzer fixture, not shipped code. It stands in for an
// undeclared listener built with chi.NewMux rather than chi.NewRouter.
package other

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// Handler builds an undeclared listener.
func Handler() http.Handler {
	r := chi.NewMux()
	r.Get("/undeclared", handler)
	return r
}
