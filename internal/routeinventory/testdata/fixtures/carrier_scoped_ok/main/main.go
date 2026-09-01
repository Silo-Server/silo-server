// Package main is an analyzer fixture, not shipped code. It must be accepted:
// the name `router` is a listener handler in main but an unrelated value in
// another function, and serve() takes the handler as http.Handler and only
// serves it.
package main

import (
	"net/http"

	"example.test/fixture/listener"
)

type fakeRouter struct{}

func (fakeRouter) Use(int) {}

func serve(addr string, h http.Handler) {
	srv := &http.Server{Addr: addr, Handler: h}
	_ = srv.ListenAndServe()
}

func unrelated() {
	router := fakeRouter{}
	router.Use(1)
}

func main() {
	router := listener.NewRouter()
	unrelated()
	serve(":8080", router)
}
