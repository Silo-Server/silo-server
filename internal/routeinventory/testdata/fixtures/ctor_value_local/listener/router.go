// Package listener is an analyzer fixture, not shipped code. The constructor
// is called through a function value, so the call is not spelled chi.NewRouter().
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}
func hidden(w http.ResponseWriter, r *http.Request)  {}

// NewRouter is the fixture listener entry point.
func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	ctor := chi.NewRouter
	sub := ctor()
	sub.Get("/hidden", hidden)
	r.Handle("/x/*", sub)
	return r
}
