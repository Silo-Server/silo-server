// Package main is an analyzer fixture, not shipped code. The route below is
// registered after the listener entry point returned. The walk never sees it,
// which is why an entry point may not hand its router out as anything but an
// http.Handler.
package main

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/entry_returns_router/listener"
)

func extra(w http.ResponseWriter, r *http.Request) {}

func main() {
	router := listener.NewRouter()
	router.Get("/registered-after-construction", extra)
	_ = http.ListenAndServe(":8080", router)
}
