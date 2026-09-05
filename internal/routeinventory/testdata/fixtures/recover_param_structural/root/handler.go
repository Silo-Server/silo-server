// Package root is an analyzer fixture, not shipped code. The root constructor
// asserts its delegated API handler to a structural interface and registers
// on it: the API listener's sealed handler makes that fail at runtime, and
// the sweep refuses the assertion regardless.
package root

import "net/http"

func metrics(w http.ResponseWriter, r *http.Request) {}
func hidden(w http.ResponseWriter, r *http.Request)  {}

// newRootHandler is the fixture root listener entry point; it seals the mux.
func newRootHandler(apiRouter http.Handler) http.Handler {
	return sealedHandler{h: newRootMux(apiRouter)}
}

// sealedHandler is the fixture's sealed return type.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

func newRootMux(apiRouter http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", http.HandlerFunc(metrics))
	if r, ok := apiRouter.(interface {
		Get(string, http.HandlerFunc)
	}); ok {
		r.Get("/api/v1/hidden", hidden)
	}
	mux.Handle("/api/", apiRouter)
	return mux
}
