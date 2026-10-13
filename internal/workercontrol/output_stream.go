package workercontrol

import (
	"errors"
	"net/http"
	"time"
)

type outputRequestKey struct{}
type outputRequestAuthority struct {
	work        string
	generation  uint64
	epoch       string
	authorityID string
	expires     time.Time
}

// Output reads may last longer than a grant's request-admission window. Check
// continuing viewer/work authority on each write without making a core RPC.
// In particular, one revoked viewer must not stop a shared prepared artifact.
type outputFencedWriter struct {
	http.ResponseWriter
	runtime *Runtime
	request *http.Request
}

func (w *outputFencedWriter) allowed() bool {
	scope, authenticated := w.request.Context().Value(outputRequestKey{}).(outputRequestAuthority)
	if !authenticated {
		return true // Authentication failures may still return their error body.
	}
	return (scope.expires.IsZero() || time.Now().Before(scope.expires)) && (scope.epoch == "" || scope.epoch == w.runtime.config.OutputAuthorizationEpoch()) && w.runtime.workAllowed(scope.work, scope.generation) && (scope.authorityID == "" || w.runtime.authority.OutputAllowed(scope.authorityID))
}

func (w *outputFencedWriter) WriteHeader(status int) {
	if status < http.StatusBadRequest && !w.allowed() {
		panic(http.ErrAbortHandler)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *outputFencedWriter) Write(data []byte) (int, error) {
	if !w.allowed() {
		return 0, errors.New("output authority expired")
	}
	return w.ResponseWriter.Write(data)
}

func (w *outputFencedWriter) Flush() {
	if !w.allowed() {
		panic(http.ErrAbortHandler)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *outputFencedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
