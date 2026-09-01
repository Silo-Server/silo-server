// Package main is an analyzer fixture, not shipped code. A registration
// method is taken as a value and called later.
package main

import (
	"net/http"

	"example.test/fixture/listener"
)

func hidden(w http.ResponseWriter, r *http.Request) {}

func main() {
	router := listener.NewRouter()
	reg := router.Get
	reg("/hidden", hidden)
	_ = http.ListenAndServe(":8080", router)
}
