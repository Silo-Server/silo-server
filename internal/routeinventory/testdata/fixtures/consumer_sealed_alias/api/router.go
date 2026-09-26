// Package api is an analyzer fixture, not shipped code.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	v2 "github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/consumer_sealed_alias/v2"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point; it seals the router. The
// delegation registers an alias of a local that holds the v2 handler.
func NewRouter() http.Handler {
	return sealedHandler{h: newRouter()}
}

type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/api/v1/thing", handler)
	h := v2.NewHandler()
	h2 := h
	r.Handle("/api/v2/*", h2)
	return r
}
