// Package listener is an analyzer fixture, not shipped code. A second mux
// built with new() hides /x/hidden behind the /x/* wildcard rows.
package listener

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}
func hidden(w http.ResponseWriter, r *http.Request)  {}

// NewRouter is the fixture listener entry point.
func NewRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/visible", handler)
	m := new(http.ServeMux)
	m.HandleFunc("/x/hidden", hidden)
	r.Handle("/x/*", m)
	return r
}
