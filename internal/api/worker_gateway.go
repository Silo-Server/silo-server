package api

import "net/http"

// workerGateway exposes only the explicitly registered internal operations.
// It preserves the complete request target: the core verifies signed method,
// URL, headers and body, and its sealed router owns the path-value extraction.
// Enrollment and output delegation keep their separate core admission gates.
type workerGateway struct{ handler http.Handler }

func (g workerGateway) enroll(w http.ResponseWriter, r *http.Request)      { g.handler.ServeHTTP(w, r) }
func (g workerGateway) outputGrant(w http.ResponseWriter, r *http.Request) { g.handler.ServeHTTP(w, r) }
func (g workerGateway) snapshot(w http.ResponseWriter, r *http.Request)    { g.handler.ServeHTTP(w, r) }
func (g workerGateway) report(w http.ResponseWriter, r *http.Request)      { g.handler.ServeHTTP(w, r) }
func (g workerGateway) recipe(w http.ResponseWriter, r *http.Request)      { g.handler.ServeHTTP(w, r) }
func (g workerGateway) closeWork(w http.ResponseWriter, r *http.Request)   { g.handler.ServeHTTP(w, r) }
func (g workerGateway) revoke(w http.ResponseWriter, r *http.Request)      { g.handler.ServeHTTP(w, r) }
func (g workerGateway) token(w http.ResponseWriter, r *http.Request)       { g.handler.ServeHTTP(w, r) }
func (g workerGateway) renew(w http.ResponseWriter, r *http.Request)       { g.handler.ServeHTTP(w, r) }
func (g workerGateway) source(w http.ResponseWriter, r *http.Request)      { g.handler.ServeHTTP(w, r) }
