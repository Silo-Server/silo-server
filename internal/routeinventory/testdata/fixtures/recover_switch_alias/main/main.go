// Package main is an analyzer fixture, not shipped code. A type switch whose
// router case is an alias and sits first, followed by another case.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/recover_switch_alias/listener"
)

type routerAlias = chi.Router

func extra(w http.ResponseWriter, r *http.Request) {}

func late(h http.Handler) {
	switch r := h.(type) {
	case routerAlias:
		r.Get("/hidden", extra)
	case http.HandlerFunc:
		_ = r
	}
}

func main() {
	h := listener.NewRouter()
	late(h)
	_ = http.ListenAndServe(":8080", h)
}
