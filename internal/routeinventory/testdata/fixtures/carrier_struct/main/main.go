// Package main is an analyzer fixture, not shipped code. The listener handler
// is stored in a struct literal and registered on through the field.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"example.test/fixture/listener"
)

type app struct{ h http.Handler }

func hidden(w http.ResponseWriter, r *http.Request) {}

func main() {
	a := &app{h: listener.NewRouter()}
	a.h.(chi.Router).Get("/hidden", hidden)
	_ = http.ListenAndServe(":8080", a.h)
}
