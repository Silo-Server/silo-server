package apiv2

import (
	"net/http"
)

// streamResponseWriter adapts legacy byte handlers to the v2 problem format.
// It owns commitment tracking so every byte endpoint handles pre-body errors,
// late failures, and flushing the same way.
type streamResponseWriter struct {
	http.ResponseWriter
	request       *http.Request
	problemType   func(int) ProblemType
	redactHeaders []string
	status        int
	rejected      bool
}

func (w *streamResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *streamResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		if status >= 400 && !w.rejected {
			panic(http.ErrAbortHandler)
		}
		return
	}
	if status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	if status < 400 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.rejected = true
	for _, header := range w.redactHeaders {
		w.Header().Del(header)
	}
	kind := w.problemType(status)
	writeProblem(w.ResponseWriter, w.request, NewProblem(kind, kind.Title))
}

func (w *streamResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.rejected {
		return len(data), nil
	}
	return w.ResponseWriter.Write(data)
}

func (w *streamResponseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
