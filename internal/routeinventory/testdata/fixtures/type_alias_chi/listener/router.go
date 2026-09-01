// Package listener is an analyzer fixture, not shipped code. A type alias
// gives chi.Router a second name the unreached-helper audit does not match.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Router is chi.Router under another name.
type Router = chi.Router

func handler(w http.ResponseWriter, r *http.Request) {}
func hidden(w http.ResponseWriter, r *http.Request)  {}

func extras(r Router) {
	r.Get("/hidden", hidden)
}

// NewRouter is the fixture listener entry point.
func NewRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	return r
}
