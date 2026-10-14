package workerinput

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestSourceTransferResumesExactRemainingRange(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	var calls atomic.Int64
	var rangesMu sync.Mutex
	var ranges []string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangesMu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		rangesMu.Unlock()
		if r.Header.Get("If-Match") != `"r"` || r.Header.Get("X-Silo-Source-Grant") != "g" {
			t.Error("missing immutable source proof")
		}
		start, end := int64(0), int64(len(data)-1)
		status := 200
		if value := r.Header.Get("Range"); value != "" {
			var e error
			start, end, e = requestedRange(value, int64(len(data)))
			if e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			status = 206
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		}
		w.Header().Set("ETag", `"r"`)
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(status)
		if calls.Add(1) == 1 {
			_, _ = w.Write(data[start : start+16384])
			return
		}
		_, _ = w.Write(data[start : end+1])
	}))
	defer origin.Close()
	a, e := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = a.Close() }()
	h, e := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: int64(len(data)), WorkID: "work", Generation: 1})
	if e != nil {
		t.Fatal(e)
	}
	response, e := http.Get(h.URL())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = response.Body.Close() }()
	got, e := io.ReadAll(response.Body)
	if e != nil || !bytes.Equal(data, got) {
		t.Fatalf("resumed output: bytes=%d/%d err=%v", len(got), len(data), e)
	}
	rangesMu.Lock()
	defer rangesMu.Unlock()
	if len(ranges) != 2 || ranges[0] != "" || ranges[1] != "bytes=16384-131071" {
		t.Fatalf("cursor ranges=%v", ranges)
	}
}
func TestSourceTransferRetryCancellationReleasesReader(t *testing.T) {
	received := make(chan struct{}, 1)
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case received <- struct{}{}:
		default:
		}
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer origin.Close()
	a, e := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }, MaxReads: 1})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = a.Close() }()
	h, e := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, "GET", h.URL(), nil)
		r, e := http.DefaultClient.Do(req)
		if e == nil {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
		}
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("no source request")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled retry did not join")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(a.reads) != 0 {
		select {
		case <-deadline.C:
			t.Fatal("reader quota not released")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if h.Validate(t.Context()) != nil {
		t.Fatal("caller cancellation retired shared handle")
	}
}

func TestSourceTransferRetriesStopAtOriginalSignedExpiry(t *testing.T) {
	first := make(chan struct{}, 1)
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case first <- struct{}{}:
		default:
		}
		w.Header().Set("Retry-After", "1")
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer origin.Close()
	var handle *Handle
	authority, err := workerwire.NewLocalAuthority(7, "boot", "nonce", func(string, uint64) {
		if handle != nil {
			_ = handle.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	snapshot := workerwire.Snapshot{Version: 1, NodeID: 7, BootID: "boot", Nonce: "nonce", Sequence: 1, Active: map[string]uint64{"work": 1}, ValidForMillis: 2000}
	if err = authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: authority.Allowed, MaxReads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	handle, err = a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence = 2
	snapshot.ValidForMillis = 75
	if err = authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := http.Get(handle.URL())
		if err == nil {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
		}
	}()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("no initial attempt")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("retry outlived signed expiry")
	}
	if calls.Load() != 1 || len(a.reads) != 0 || authority.Allowed("work", 1) {
		t.Fatalf("expired retries=%d quota=%d", calls.Load(), len(a.reads))
	}
}

func TestSourceTransferDefinitiveLossDuringRetryRetiresHandle(t *testing.T) {
	for _, loss := range []string{"403", "410", "412", "etag", "range"} {
		t.Run(loss, func(t *testing.T) {
			var calls atomic.Int64
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					http.Error(w, "temporary", http.StatusServiceUnavailable)
					return
				}
				if loss == "403" || loss == "410" || loss == "412" {
					code, _ := strconv.Atoi(loss)
					http.Error(w, "withdrawn", code)
					return
				}
				etag, representation := `"r"`, "bytes 1-3/10"
				if loss == "etag" {
					etag = `"changed"`
				}
				if loss == "range" {
					representation = "bytes 2-4/10"
				}
				w.Header().Set("ETag", etag)
				w.Header().Set("Content-Range", representation)
				w.Header().Set("Content-Length", "3")
				w.WriteHeader(206)
				_, _ = io.WriteString(w, "123")
			}))
			defer origin.Close()
			a, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			h, err := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("GET", h.URL(), nil)
			req.Header.Set("Range", "bytes=1-3")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(body, []byte("123")) || response.StatusCode < 400 || h.Validate(t.Context()) == nil || calls.Load() != 2 {
				t.Fatalf("definitive loss escaped: status=%d body=%q calls=%d", response.StatusCode, body, calls.Load())
			}
		})
	}
}

func TestSourceTransferTransientExhaustionRetainsHandle(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "temporary", status) }))
			defer origin.Close()
			a, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, Allowed: func(string, uint64) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			h, err := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.Get(h.URL())
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 503 || response.Header.Get("Retry-After") == "" || h.Validate(t.Context()) != nil || calls.Load() != sourceTransferAttempts {
				t.Fatalf("transient status=%d calls=%d", response.StatusCode, calls.Load())
			}
		})
	}
}
