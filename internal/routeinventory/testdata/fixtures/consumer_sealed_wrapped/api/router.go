// Package api is an analyzer fixture, not shipped code.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	v2 "github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/consumer_sealed_wrapped/v2"
)

func handler(w http.ResponseWriter, r *http.Request) {}

// NewRouter is the fixture listener entry point; it seals the router. The
// delegation wraps the v2 handler in another call instead of registering it.
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
	r.Handle("/api/v2/*", http.StripPrefix("", v2.NewHandler()))
	return r
}
