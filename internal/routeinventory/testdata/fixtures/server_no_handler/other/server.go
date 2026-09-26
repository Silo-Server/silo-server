// Package other is an analyzer fixture, not shipped code. An http.Server with
// no Handler serves http.DefaultServeMux.
package other

import "net/http"

// Serve serves the default mux through an http.Server.
func Serve() {
	srv := &http.Server{Addr: ":6060"}
	_ = srv.ListenAndServe()
}
