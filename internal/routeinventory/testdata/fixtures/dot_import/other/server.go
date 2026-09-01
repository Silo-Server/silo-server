// Package other is an analyzer fixture, not shipped code. A dot-import lets
// the constructor be called unqualified.
package other

import (
	"net/http"

	. "github.com/go-chi/chi/v5"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

// Serve builds and serves an undeclared listener.
func Serve() {
	r := NewRouter()
	r.Get("/hidden", hidden)
	_ = http.ListenAndServe(":9090", r)
}
