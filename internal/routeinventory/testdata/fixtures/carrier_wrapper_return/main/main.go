// Package main is an analyzer fixture, not shipped code. A same-package
// wrapper returns the listener handler, so the name bound from the wrapper is
// the handler under another spelling.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"example.test/fixture/listener"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func build() http.Handler {
	return listener.NewRouter()
}

func main() {
	router := build()
	router.(chi.Router).Get("/hidden", hidden)
	_ = http.ListenAndServe(":8080", router)
}
