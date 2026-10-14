package workerinput

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTransientIndexMustNotCloseSharedHandle(t *testing.T) {
	for _, mode := range []string{"500", "503", "short-body"} {
		t.Run(mode, func(t *testing.T) {
			var fail atomic.Bool
			fail.Store(true)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fail.Load() && mode != "short-body" {
					code := 500
					if mode == "503" {
						code = 503
					}
					http.Error(w, "temporary", code)
					return
				}
				w.Header().Set("ETag", `"r"`)
				start, end := int64(0), int64(9)
				if r.Header.Get("Range") != "" {
					start, end, _ = requestedRange(r.Header.Get("Range"), 10)
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/10", start, end))
				}
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				if r.Header.Get("Range") != "" {
					w.WriteHeader(206)
				}
				if fail.Load() {
					if end-start+1 > 3 {
						_, _ = io.WriteString(w, "0123456789"[start:start+3])
					}
					return
				}
				_, _ = io.WriteString(w, "0123456789"[start:end+1])
			}))
			defer origin.Close()
			a, e := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = a.Close() }()
			h, e := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
			if e != nil {
				t.Fatal(e)
			}
			reader, e := h.ReaderAt(t.Context(), ReaderLimits{})
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = reader.Close() }()
			buf := make([]byte, 4)
			_, readErr := reader.ReadAt(buf, 0)
			if readErr == nil {
				t.Fatal("fault not observed")
			}
			fail.Store(false)
			next, e := http.Get(h.URL())
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = next.Body.Close() }()
			_, _ = io.Copy(io.Discard, next.Body)
			validErr := h.Validate(context.Background())
			t.Logf("mode=%s initialErr=%v handleErr=%v nextFFmpegHTTP=%d", mode, readErr, validErr, next.StatusCode)
			if validErr != nil || next.StatusCode == 404 {
				t.Fatalf("optional index failure permanently closed FFmpeg handle")
			}
		})
	}
}
func TestTruncatedFFmpegBodyMustNotRetireHandle(t *testing.T) {
	var count atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := int64(0), int64(9)
		status := http.StatusOK
		if value := r.Header.Get("Range"); value != "" {
			start, end, _ = requestedRange(value, 10)
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/10", start, end))
		}
		w.Header().Set("ETag", `"r"`)
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(status)
		if count.Add(1) == 1 {
			_, _ = io.WriteString(w, "abc")
			return
		}
		_, _ = io.WriteString(w, "0123456789"[start:end+1])
	}))
	defer origin.Close()
	a, e := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = a.Close() }()
	h, e := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
	if e != nil {
		t.Fatal(e)
	}
	first, e := http.Get(h.URL())
	if e == nil {
		defer func() { _ = first.Body.Close() }()
		_, e = io.Copy(io.Discard, first.Body)
	}
	next, nextErr := http.Get(h.URL())
	if nextErr != nil {
		t.Fatal(nextErr)
	}
	defer func() { _ = next.Body.Close() }()
	data, bodyErr := io.ReadAll(next.Body)
	t.Logf("firstErr=%v restored nextHTTP=%d nextBody=%q nextErr=%v", e, next.StatusCode, data, bodyErr)
	if next.StatusCode != 200 || strings.TrimSpace(string(data)) != "0123456789" {
		t.Fatalf("truncation retired shared handle: HTTP%d", next.StatusCode)
	}
}
