package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Progressive responses (streamed subtitle extracts, remux output) rely on
// http.Flusher reaching the real connection through every wrapper in the
// middleware chain. A wrapper that drops Flush silently degrades streaming
// to whole-response buffering, so assert the full chain forwards it.
func TestMiddlewareChainForwardsFlush(t *testing.T) {
	var sawFlusher bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			_, _ = w.Write([]byte("chunk"))
			f.Flush()
		}
	})

	// Same wrapping order as the API router: RequestLogger outermost, then
	// Metrics; the handler sees the innermost wrapper.
	chain := RequestLogger("test-node")(Metrics(handler))

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stream/x/subtitles/0.vtt", nil))

	if !sawFlusher {
		t.Fatal("handler's ResponseWriter does not implement http.Flusher through the middleware chain")
	}
	if !rec.Flushed {
		t.Fatal("Flush did not propagate to the underlying ResponseWriter")
	}
}

func TestStatusWritersRecordImplicitOKOnFlush(t *testing.T) {
	t.Run("metrics", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writer := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

		writer.Flush()
		writer.WriteHeader(http.StatusInternalServerError)

		if writer.status != http.StatusOK || !writer.written {
			t.Fatalf("status = %d, written = %v; want committed 200", writer.status, writer.written)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("response status = %d, want 200", rec.Code)
		}
	})

	t.Run("request logger", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writer := &requestStatusWriter{ResponseWriter: rec, status: http.StatusOK}

		writer.Flush()
		writer.WriteHeader(http.StatusInternalServerError)

		if writer.status != http.StatusOK || !writer.wroteHeader {
			t.Fatalf("status = %d, wroteHeader = %v; want committed 200", writer.status, writer.wroteHeader)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("response status = %d, want 200", rec.Code)
		}
	})
}
