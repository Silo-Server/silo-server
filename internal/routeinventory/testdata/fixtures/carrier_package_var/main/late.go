// Package main is an analyzer fixture, not shipped code. This file registers
// on the package-level handler; it sorts before main.go, so a per-file audit
// keyed on declaration order would never see the assignment that makes
// apiHandler a carrier.
package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func lateRegister() {
	apiHandler.(chi.Router).Get("/hidden", hidden)
}
