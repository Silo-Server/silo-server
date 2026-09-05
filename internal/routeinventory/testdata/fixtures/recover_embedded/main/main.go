// Package main is an analyzer fixture, not shipped code. It tries to recover
// the router from the sealed handler after the entry function returned.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/recover_embedded/listener"
)

func extra(w http.ResponseWriter, r *http.Request) {}

type routerEmbedded interface{ chi.Router }

func late(h http.Handler) {
	if r, ok := h.(routerEmbedded); ok {
		r.Get("/hidden", extra)
	}
}

func main() {
	h := listener.NewRouter()
	late(h)
	_ = http.ListenAndServe(":8080", h)
}
