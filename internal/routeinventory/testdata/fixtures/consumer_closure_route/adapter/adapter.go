// Package adapter is an analyzer fixture, not shipped code: it stands in for
// the framework adapter constructor that consumes a listener's router.
package adapter

import "github.com/go-chi/chi/v5"

// New consumes the router and registers operations on it.
func New(r chi.Router, n int) int { return n }
