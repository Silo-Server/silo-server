// Package main is an analyzer fixture, not shipped code. The helper takes the
// listener handler as http.Handler — the one parameter type the audit vouches
// for — and asserts it straight back to chi.Router.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"example.test/fixture/listener"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func wire(h http.Handler) {
	h.(chi.Router).Get("/hidden", hidden)
}

func main() {
	router := listener.NewRouter()
	wire(router)
	_ = http.ListenAndServe(":8080", router)
}
