// Package other is an analyzer fixture, not shipped code. It sits outside the
// audited packages; a router-typed field here is still a place a router can be
// kept and registered on.
package other

import "github.com/go-chi/chi/v5"

// Holder keeps a router.
type Holder struct {
	R chi.Router
}
