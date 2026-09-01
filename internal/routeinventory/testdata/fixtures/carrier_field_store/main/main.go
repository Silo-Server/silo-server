// Package main is an analyzer fixture, not shipped code. The listener handler
// is stored into a struct field and registered on through it.
package main

import (
	"net/http"

	"example.test/fixture/listener"
)

type app struct{ h http.Handler }

func hidden(w http.ResponseWriter, r *http.Request) {}

func main() {
	router := listener.NewRouter()
	a := &app{}
	a.h = router
	router.Get("/hidden", hidden)
	_ = http.ListenAndServe(":8080", a.h)
}
