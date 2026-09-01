// Package listener is an analyzer fixture, not shipped code. The constructor is
// handed to a builder as a value; the builder calls it and registers.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}
func hidden(w http.ResponseWriter, r *http.Request)  {}

func build(mk func() *chi.Mux) http.Handler {
	sub := mk()
	sub.Get("/hidden", hidden)
	return sub
}

// NewRouter is the fixture listener entry point.
func NewRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	r.Handle("/x/*", build(chi.NewRouter))
	return r
}
