// Package root is an analyzer fixture, not shipped code. The mux is built
// without http.NewServeMux(); a zero http.ServeMux is fully functional, so the
// registrations on it are live routes the walk would otherwise never bind.
package root

import "net/http"

func metrics(w http.ResponseWriter, r *http.Request) {}

// newRootHandler is the fixture root listener entry point.
func newRootHandler() http.Handler {
	mux := new(http.ServeMux)
	mux.Handle("/metrics", http.HandlerFunc(metrics))
	return mux
}
