package workerinput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func rangeReaderFixture(t *testing.T, data string) (*Handle, *atomic.Bool, *atomic.Int64) {
	t.Helper()
	var active atomic.Bool
	active.Store(true)
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Silo-Source-Grant") != "opaque" || r.Header.Get("If-Match") != `"revision"` || r.Header.Get("X-Test-Proof") != "signed" {
			t.Error("unscoped reader")
		}
		start, end, err := requestedRange(r.Header.Get("Range"), int64(len(data)))
		if err != nil {
			t.Error(err)
			http.Error(w, "bad range", 400)
			return
		}
		w.Header().Set("ETag", `"revision"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, data[start:end+1])
	}))
	t.Cleanup(origin.Close)
	adapter, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return active.Load() }, Sign: func(r *http.Request) error { r.Header.Set("X-Test-Proof", "signed"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	handle, err := adapter.Open(t.Context(), Source{ID: "stable", Revision: "revision", URL: origin.URL + "/source", Grant: "opaque", ETag: `"revision"`, Size: int64(len(data)), WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return handle, &active, &calls
}
func TestReaderAtUsesScopedRangesAndChecksAuthorityBeforeCachedBytes(t *testing.T) {
	handle, active, calls := rangeReaderFixture(t, strings.Repeat("0123456789", 100))
	reader, err := handle.ReaderAt(t.Context(), ReaderLimits{ReadAheadBytes: 64, CacheBytes: 128, MaxRangeBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	var b [10]byte
	if n, err := reader.ReadAt(b[:], 10); n != 10 || err != nil || string(b[:]) != "0123456789" {
		t.Fatalf("range n=%d bytes=%q err=%v", n, b, err)
	}
	if n, err := reader.ReadAt(b[:], 20); n != 10 || err != nil || calls.Load() != 1 {
		t.Fatalf("cache n=%d calls=%d err=%v", n, calls.Load(), err)
	}
	active.Store(false)
	if n, err := reader.ReadAt(b[:], 20); n != 0 || err == nil || calls.Load() != 1 {
		t.Fatalf("revoked cache n=%d calls=%d err=%v", n, calls.Load(), err)
	}
}
func TestReaderAtBoundsAndBudgetDoNotRevokeSource(t *testing.T) {
	handle, _, calls := rangeReaderFixture(t, strings.Repeat("x", 1024))
	reader, err := handle.ReaderAt(t.Context(), ReaderLimits{ReadAheadBytes: 32, CacheBytes: 64, MaxRangeBytes: 64, MaxBytes: 32, MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	var b [4]byte
	if _, err := reader.ReadAt(b[:], -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if n, err := reader.ReadAt(b[:], 1024); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF n=%d err=%v", n, err)
	}
	if _, err := reader.ReadAt(b[:], 0); err != nil {
		t.Fatal(err)
	}
	if n, err := reader.ReadAt(b[:], 100); n != 0 || !errors.Is(err, ErrReaderLimit) || calls.Load() != 1 {
		t.Fatalf("budget n=%d err=%v calls=%d", n, err, calls.Load())
	}
	if err := handle.Validate(t.Context()); err != nil {
		t.Fatalf("budget revoked whole source: %v", err)
	}
}
func TestReaderAtCancellationReleasesBlockedNetworkRead(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"r"`)
		w.Header().Set("Content-Range", "bytes 0-9/10")
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(done)
	}))
	defer origin.Close()
	adapter, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	handle, err := adapter.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "w", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, err := handle.ReaderAt(ctx, ReaderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	result := make(chan error, 1)
	go func() { var b [4]byte; _, err := reader.ReadAt(b[:], 0); result <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reader did not open source")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("upstream not canceled")
	}
	if err := handle.Validate(t.Context()); err != nil {
		t.Fatalf("caller cancellation revoked source: %v", err)
	}
}
func TestReaderAtRejectsWrongRangesAndRetainsShortBodyAuthority(t *testing.T) {
	for _, test := range []struct{ name, etag, contentRange, body string }{{"etag", `"wrong"`, "bytes 0-9/10", "0123456789"}, {"range", `"r"`, "bytes 1-9/10", "0123456789"}, {"shortbody", `"r"`, "bytes 0-9/10", "abc"}} {
		t.Run(test.name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", test.etag)
				contentRange, body, length := test.contentRange, test.body, int64(10)
				if test.name == "shortbody" {
					start, end, _ := requestedRange(r.Header.Get("Range"), 10)
					contentRange = fmt.Sprintf("bytes %d-%d/10", start, end)
					length = end - start + 1
					body = strings.Repeat("x", int(min(3, length-1)))
				}
				w.Header().Set("Content-Range", contentRange)
				w.Header().Set("Content-Length", fmt.Sprint(length))
				w.WriteHeader(206)
				_, _ = io.WriteString(w, body)
			}))
			defer origin.Close()
			adapter, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = adapter.Close() }()
			handle, err := adapter.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "w", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := handle.ReaderAt(t.Context(), ReaderLimits{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close() }()
			var b [4]byte
			if n, err := reader.ReadAt(b[:], 0); n != 0 || err == nil {
				t.Fatalf("bad source returned bytes n=%d err=%v", n, err)
			}
			if test.name == "shortbody" {
				if handle.Validate(t.Context()) != nil {
					t.Fatal("truncated operation revoked shared source")
				}
			} else if handle.Validate(t.Context()) == nil {
				t.Fatal("corrupt source remained admitted")
			}
		})
	}
}

func TestReaderAtConcurrentAdmissionIsBounded(t *testing.T) {
	handle, _, _ := rangeReaderFixture(t, strings.Repeat("x", 128))
	var readers []*RangeReader
	defer func() {
		for _, reader := range readers {
			_ = reader.Close()
		}
	}()
	for range cap(readerSlots) {
		reader, err := handle.ReaderAt(t.Context(), ReaderLimits{})
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, reader)
	}
	if _, err := handle.ReaderAt(t.Context(), ReaderLimits{}); !errors.Is(err, ErrReaderLimit) {
		t.Fatalf("unbounded admission: %v", err)
	}
	if err := handle.Validate(t.Context()); err != nil {
		t.Fatalf("admission limit revoked source: %v", err)
	}
}
func TestReaderAtHandleCloseAndDeadlineStopNetworkRead(t *testing.T) {
	for _, mode := range []string{"handle_close", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			done := make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"r"`)
				w.Header().Set("Content-Range", "bytes 0-9/10")
				w.Header().Set("Content-Length", "10")
				w.WriteHeader(206)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(done)
			}))
			defer origin.Close()
			adapter, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = adapter.Close() }()
			handle, err := adapter.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "w", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			limits := ReaderLimits{}
			if mode == "deadline" {
				limits.Timeout = 100 * time.Millisecond
			}
			reader, err := handle.ReaderAt(t.Context(), limits)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close() }()
			result := make(chan error, 1)
			go func() { var b [4]byte; _, err := reader.ReadAt(b[:], 0); result <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("read did not begin")
			}
			if mode == "handle_close" {
				_ = handle.Close()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled read succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("read did not stop")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("upstream not canceled")
			}
		})
	}
}
