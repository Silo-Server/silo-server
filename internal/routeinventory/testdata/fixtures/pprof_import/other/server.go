// Package other is an analyzer fixture, not shipped code. net/http/pprof
// registers /debug/pprof/ on http.DefaultServeMux at init.
package other

import (
	"net/http"
	_ "net/http/pprof"
)

// Serve serves the default mux, pprof routes included.
func Serve() {
	_ = http.ListenAndServe(":6060", nil)
}
