// Package main is an analyzer fixture, not shipped code.
package main

import (
	"net/http"

	"example.test/fixture/listener"
)

var apiHandler http.Handler

func main() {
	apiHandler = listener.NewRouter()
	lateRegister()
	_ = http.ListenAndServe(":8080", apiHandler)
}
