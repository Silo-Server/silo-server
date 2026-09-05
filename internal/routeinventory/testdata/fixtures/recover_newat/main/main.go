// Package main is an analyzer fixture, not shipped code. It rebuilds a pointer
// to the router behind the sealed handler with reflect alone: no unsafe
// import, no type assertion, no MethodByName.
package main

import (
	"net/http"

	"reflect"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/recover_newat/listener"
)

func extra(w http.ResponseWriter, r *http.Request) {}

func late(h http.Handler) {
	f := reflect.ValueOf(h).Field(0).Elem()
	mux := reflect.NewAt(f.Type().Elem(), f.UnsafePointer())
	for i := 0; i < mux.NumMethod(); i++ {
		if mux.Type().Method(i).Name == "Get" {
			mux.Method(i).Call([]reflect.Value{reflect.ValueOf("/hidden"), reflect.ValueOf(http.HandlerFunc(extra))})
		}
	}
}

func main() {
	h := listener.NewRouter()
	late(h)
	_ = http.ListenAndServe(":8080", h)
}
