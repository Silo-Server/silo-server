// Package v2 is an analyzer fixture, not shipped code: a listener whose router
// is handed once to a declared consumer that registers its operations.
package v2

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/routeinventory/testdata/fixtures/consumer_ok/adapter"
)

// NewHandler is the fixture entry point; it seals the router.
func NewHandler() http.Handler {
	return sealedHandler{h: newChiRouter()}
}

type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newChiRouter() chi.Router {
	r := chi.NewRouter()
	api := adapter.New(r, 1)
	_ = api
	return r
}
