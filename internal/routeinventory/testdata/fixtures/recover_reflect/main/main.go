// Package main is an analyzer fixture, not shipped code. It tries to recover
// the router from the sealed handler after the entry function returned.
package main

import (
	"net/http"

	"reflect"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/recover_reflect/listener"
)

func extra(w http.ResponseWriter, r *http.Request) {}

func late(h http.Handler) {
	m := reflect.ValueOf(h).MethodByName("Get")
	if m.IsValid() {
		m.Call([]reflect.Value{reflect.ValueOf("/hidden"), reflect.ValueOf(http.HandlerFunc(extra))})
	}
}

func main() {
	h := listener.NewRouter()
	late(h)
	_ = http.ListenAndServe(":8080", h)
}
