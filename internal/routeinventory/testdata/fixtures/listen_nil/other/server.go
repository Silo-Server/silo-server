// Package other is an analyzer fixture, not shipped code. A nil handler means
// http.DefaultServeMux, whatever anything else registered on it.
package other

import "net/http"

// Serve serves the default mux.
func Serve() {
	_ = http.ListenAndServe(":6060", nil)
}
