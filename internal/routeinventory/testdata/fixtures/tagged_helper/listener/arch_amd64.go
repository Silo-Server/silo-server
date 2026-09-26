//go:build amd64

package listener

import "github.com/go-chi/chi/v5"

// arch registers a route only in the amd64 build; the other build's arch is a
// no-op, so the generator sees whichever one its own build context selects.
func arch(r chi.Router) { r.Get("/hidden", handler) }
