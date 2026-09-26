package debugserver

import (
	"net/http"
	pprof "net/http/pprof"
)

type sealedHandler struct{ handler http.Handler }

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func newHandler() http.Handler { return sealedHandler{handler: newMux()} }

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	return mux
}

// Even the designated profiling file cannot serve the default mux.
func unsafeServe() { _ = http.ListenAndServe(":6060", nil) }
