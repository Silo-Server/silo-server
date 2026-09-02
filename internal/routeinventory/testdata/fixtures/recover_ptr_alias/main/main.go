// Package main is an analyzer fixture, not shipped code. It tries to recover
// the router through a pointer to an alias of the concrete mux type.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/recover_ptr_alias/listener"
)

type muxAlias = chi.Mux

func extra(w http.ResponseWriter, r *http.Request) {}

func late(h http.Handler) {
	if r, ok := h.(*muxAlias); ok {
		r.Get("/hidden", extra)
	}
}

func main() {
	h := listener.NewRouter()
	late(h)
	_ = http.ListenAndServe(":8080", h)
}
