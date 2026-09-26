// Package main is an analyzer fixture, not shipped code. It tries to recover
// the router from the sealed handler after the entry function returned.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/recover_generic/listener"
)

func extra(w http.ResponseWriter, r *http.Request) {}

func as[T any](h http.Handler) (T, bool) {
	v, ok := h.(T)
	return v, ok
}

func late(h http.Handler) {
	if r, ok := as[chi.Router](h); ok {
		r.Get("/hidden", extra)
	}
}

func main() {
	h := listener.NewRouter()
	late(h)
	_ = http.ListenAndServe(":8080", h)
}
