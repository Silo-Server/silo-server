// Package listener is an analyzer fixture, not shipped code. A zero
// http.ServeMux at package scope is a working router nothing constructs.
package listener

import "net/http"

// Debug is a live mux with no constructor call anywhere.
var Debug http.ServeMux

func init() {
	Debug.HandleFunc("/debug/hidden", handler)
}
