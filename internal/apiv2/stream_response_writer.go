package apiv2

import (
	"io"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/httpstream"
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

// ReadFrom keeps the sendfile path for file bodies. io.Copy finds
// io.ReaderFrom only by direct assertion, so without it every byte behind this
// writer goes through a 32 KB buffer. http.ServeContent commits its status
// before copying, so a file body reaches the wrapped writer's ReadFrom.
//
// A copy that starts before any status goes through Write instead. Forwarding
// it would let the wrapped writer commit 200 without this writer knowing, so a
// later failure would append a problem body to the media instead of aborting;
// committing 200 first would lose the problem response of a source that fails
// before its first byte. Write commits only on the first byte, and discards
// the body after a rejected status.
func (w *streamResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 || w.rejected {
		return io.Copy(httpstream.WriterOnly(w), src)
	}
	return httpstream.ForwardReadFrom(w.ResponseWriter, w, src, 0, nil)
}

func (w *streamResponseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
