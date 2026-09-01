// Package main is an analyzer fixture, not shipped code. The listener handler
// is bound with `var`, which is a local variable like any other.
package main

import (
	"net/http"

	"example.test/fixture/listener"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func main() {
	var router = listener.NewRouter()
	router.Get("/hidden", hidden)
	_ = http.ListenAndServe(":8080", router)
}
