package handlers

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// HandleSubtitle serves the canonical track or a receiver-specific WebVTT
// timeline. Transforming after extraction keeps the shared cache canonical.
func (h *StreamHandler) HandleSubtitle(w http.ResponseWriter, r *http.Request) {
	values, present := r.URL.Query()["timestamp_offset"]
	if !present {
		h.handleSubtitle(w, r)
		return
	}
	var offset float64
	var err error
	if len(values) == 1 {
		offset, err = strconv.ParseFloat(values[0], 64)
	}
	_, format, formatErr := playback.ParseSubtitleTrackParam(chi.URLParam(r, "track"))
	if len(values) != 1 || err != nil || math.IsNaN(offset) || math.IsInf(offset, 0) || math.Abs(offset) > 31536000 || formatErr != nil || format != subtitleFormatVTTV3 {
		writeError(w, http.StatusBadRequest, "bad_request", "timestamp_offset requires a single finite number of seconds within one year and a .vtt subtitle")
		return
	}
	if offset == 0 {
		h.handleSubtitle(w, r)
		return
	}

	r = r.Clone(r.Context())
	for _, name := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match", "If-Unmodified-Since", "If-Match"} {
		r.Header.Del(name)
	}
	response := &subtitleOffsetResponseWriter{ResponseWriter: w, head: r.Method == http.MethodHead}
	response.transform = subtitles.NewWebVTTOffsetWriter(subtitleOffsetSink{response}, time.Duration(offset*float64(time.Second)))
	h.handleSubtitle(response, r)
	// An upstream panic must not close a partial document into a clean response.
	if response.err == nil && response.status == http.StatusOK && !response.head {
		response.err = response.transform.Close()
	}
	if response.err != nil {
		panic(http.ErrAbortHandler)
	}
}

type subtitleOffsetResponseWriter struct {
	http.ResponseWriter
	transform io.WriteCloser
	status    int
	head      bool
	err       error
}

// Unwrap lets the rolling stream deadline reach the underlying connection.
func (w *subtitleOffsetResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *subtitleOffsetResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status == http.StatusOK {
		for _, name := range []string{"Content-Length", "ETag", "Last-Modified", "Content-Range", "Accept-Ranges"} {
			w.Header().Del(name)
		}
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *subtitleOffsetResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.head {
		return len(data), nil
	}
	if w.err != nil {
		return 0, w.err
	}
	var n int
	if w.status == http.StatusOK {
		n, w.err = w.transform.Write(data)
	} else {
		n, w.err = w.ResponseWriter.Write(data)
	}
	return n, w.err
}

// Flush never forces an incomplete cue out of the transform's buffer. The
// sink flushes only bytes the transform has accepted as a complete block.
func (w *subtitleOffsetResponseWriter) Flush() {}

type subtitleOffsetSink struct{ response *subtitleOffsetResponseWriter }

func (s subtitleOffsetSink) Write(data []byte) (int, error) {
	n, err := s.response.ResponseWriter.Write(data)
	if err == nil {
		if flusher, ok := s.response.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return n, err
}
