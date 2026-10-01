// Package other is an analyzer fixture, not shipped code. It stands in for a
// hidden listener whose router is built through a package-level function value.
package other

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

var newRouter = chi.NewRouter

func hidden(w http.ResponseWriter, r *http.Request) {}

// Serve builds and serves an undeclared listener.
func Serve() {
	r := newRouter()
	r.Get("/hidden", hidden)
	_ = http.ListenAndServe(":9090", r)
}
