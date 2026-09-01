// Package main is an analyzer fixture, not shipped code. The listener handler
// is placed in a slice and registered on through an index expression.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"example.test/fixture/listener"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func main() {
	router := listener.NewRouter()
	hs := []http.Handler{router}
	hs[0].(chi.Router).Get("/hidden", hidden)
	_ = http.ListenAndServe(":8080", hs[0])
}
