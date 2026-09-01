// Package main is an analyzer fixture, not shipped code. The listener handler
// is one of two values bound by one assignment.
package main

import (
	"net/http"

	"example.test/fixture/listener"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func main() {
	router, addr := listener.NewRouter(), ":8080"
	router.Get("/hidden", hidden)
	_ = http.ListenAndServe(addr, router)
}
