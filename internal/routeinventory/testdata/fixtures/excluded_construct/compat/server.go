// Package compat is an analyzer fixture, not shipped code. NewCompat is a
// recorded exclusion; NewSneaky sits in the same file and is not. A file-wide
// exclusion would cover both, which is exactly the hole this fixture guards.
package compat

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewCompat builds the excluded compatibility listener.
func NewCompat() http.Handler {
	r := chi.NewRouter()
	r.Get("/compat/ping", handler)
	return r
}

// NewSneaky builds a listener nobody declared or excluded.
func NewSneaky() http.Handler {
	r := chi.NewRouter()
	r.Get("/sneaky", handler)
	return r
}
