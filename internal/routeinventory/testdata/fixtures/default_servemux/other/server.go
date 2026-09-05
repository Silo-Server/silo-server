// Package other is an analyzer fixture, not shipped code. It registers on the
// default mux without spelling http.Handle or http.HandleFunc.
package other

import "net/http"

func hidden(w http.ResponseWriter, r *http.Request) {}

// Serve registers on and serves http.DefaultServeMux.
func Serve() {
	http.DefaultServeMux.HandleFunc("/hidden", hidden)
	_ = http.ListenAndServe(":9090", http.DefaultServeMux)
}
