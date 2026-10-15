package nodesource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// injection replaces the core's next source answer: a status, or the first
// cutAfter bytes of the body followed by a broken connection. stall holds
// the body after cutAfter bytes until the request ends instead; hang holds
// the request before any answer, and with a status, after the status line
// and before its body.
type injection struct {
	status   int
	cutAfter int64
	stall    bool
	hang     bool
}

// fakeCore is a core's open and source endpoints for one file.
type fakeCore struct {
	t       *testing.T
	path    string
	data    []byte
	modTime time.Time
	etag    string
	server  *httptest.Server

	mu sync.Mutex
	// latency delays every source answer, as a distant core would.
	latency    time.Duration
	inject     []injection
	ranges     []string
	grants     []string
	openQueue  []int
	openETag   string
	opens      int
	inFlight   int
	sourceDone chan struct{}
}

func newFakeCore(t *testing.T, size int) *fakeCore {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeCore{t: t, path: path, data: data, modTime: info.ModTime(), sourceDone: make(chan struct{}, 1024)}
	c.etag = Revision{Kind: KindFile, ID: 1, Size: info.Size(), Mtime: info.ModTime().Unix()}.ETag()
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.server.Close)
	return c
}

func (c *fakeCore) queue(injections ...injection) {
	c.mu.Lock()
	c.inject = append(c.inject, injections...)
	c.mu.Unlock()
}

func (c *fakeCore) queueOpens(statuses ...int) {
	c.mu.Lock()
	c.openQueue = append(c.openQueue, statuses...)
	c.mu.Unlock()
}

func (c *fakeCore) seenRanges() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ranges...)
}

func (c *fakeCore) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

func (c *fakeCore) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == PathOpen {
		c.mu.Lock()
		c.opens++
		n, status, etag := c.opens, 0, c.etag
		if len(c.openQueue) > 0 {
			status, c.openQueue = c.openQueue[0], c.openQueue[1:]
		}
		if c.openETag != "" && n > 1 {
			etag = c.openETag
		}
		c.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			http.Error(w, http.StatusText(status), status)
			return
		}
		_ = json.NewEncoder(w).Encode(OpenResponse{
			Grant: "grant-" + strconv.Itoa(n), Path: c.path, Size: int64(len(c.data)), Mtime: c.modTime.Unix(), ETag: etag,
		})
		return
	}
	grant := r.Header.Get(HeaderSourceGrant)
	if r.URL.Path != PathSources || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	c.inFlight++
	c.ranges = append(c.ranges, r.Header.Get("Range"))
	c.grants = append(c.grants, grant)
	var next injection
	if len(c.inject) > 0 {
		next, c.inject = c.inject[0], c.inject[1:]
	}
	latency := c.latency
	c.mu.Unlock()
	time.Sleep(latency)
	defer func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
		c.sourceDone <- struct{}{}
	}()
	if r.Header.Get("If-Match") != c.etag {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	if next.hang && next.status == 0 {
		<-r.Context().Done()
		return
	}
	if next.status != 0 {
		if next.hang {
			w.WriteHeader(next.status)
			_ = http.NewResponseController(w).Flush()
			<-r.Context().Done()
			return
		}
		http.Error(w, http.StatusText(next.status), next.status)
		return
	}
	if next.cutAfter > 0 || next.stall {
		start, length, _, _ := parseRange(r.Header.Get("Range"), int64(len(c.data)))
		w.Header().Set("ETag", c.etag)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, len(c.data)))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(c.data[start : start+min(next.cutAfter, length)])
		_ = http.NewResponseController(w).Flush()
		if next.stall {
			<-r.Context().Done()
			return
		}
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("ETag", c.etag)
	http.ServeContent(w, r, "", c.modTime, bytes.NewReader(c.data))
}

// postOpen is a relay's Open against a test core's open endpoint, unsigned:
// what nodecontrol.Client.OpenSource sends, without the node signature the
// test core does not check.
func postOpen(coreURL string) OpenFunc {
	return func(ctx context.Context, grant string) (*OpenResponse, error) {
		body, err := json.Marshal(OpenRequest{Grant: grant})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+PathOpen, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, &StatusError{Op: "open", Status: resp.StatusCode}
		}
		var opened OpenResponse
		if err := json.NewDecoder(resp.Body).Decode(&opened); err != nil {
			return nil, err
		}
		return &opened, nil
	}
}

func newTestRelay(t *testing.T, core *fakeCore, mode string) *Relay {
	t.Helper()
	relay, err := NewRelay(RelayOptions{Open: postOpen(core.server.URL), CoreURL: core.server.URL, NodeID: 1, NodeKey: "snk1_1_test",
		Mode: func() string { return mode }})
	if err != nil {
		t.Fatal(err)
	}
	relay.retry = backoff{initial: 2 * time.Millisecond, max: 10 * time.Millisecond, giveUp: 300 * time.Millisecond, stall: 10 * time.Second, idle: 10 * time.Second}
	relay.renewRetry = backoff{initial: 2 * time.Millisecond, max: 10 * time.Millisecond}
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

func openRemote(t *testing.T, relay *Relay) *Handle {
	t.Helper()
	h, err := relay.Open(t.Context(), "dispatched-grant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if !h.Remote() || h.Input() != h.URL() || !strings.HasPrefix(h.URL(), "http://127.0.0.1:") {
		t.Fatalf("remote handle input %q", h.Input())
	}
	return h
}

// answer is a relay answer whose body get read.
type answer struct {
	StatusCode    int
	Header        http.Header
	ContentLength int64
}

func get(t *testing.T, ctx context.Context, url, method, rangeHeader string) (answer, []byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return answer{}, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return answer{StatusCode: resp.StatusCode, Header: resp.Header, ContentLength: resp.ContentLength}, body, err
}

func TestRelayServesTheSourceToFFmpegWithRanges(t *testing.T) {
	core := newFakeCore(t, 100_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	size := int64(len(core.data))
	for _, test := range []struct {
		rangeHeader  string
		status       int
		start, end   int64
		contentRange string
	}{
		{"", http.StatusOK, 0, size, ""},
		{"bytes=10-19", http.StatusPartialContent, 10, 20, fmt.Sprintf("bytes 10-19/%d", size)},
		{"bytes=-5", http.StatusPartialContent, size - 5, size, fmt.Sprintf("bytes %d-%d/%d", size-5, size-1, size)},
		{"bytes=99000-", http.StatusPartialContent, 99000, size, fmt.Sprintf("bytes 99000-%d/%d", size-1, size)},
		{"bytes=99000-200000", http.StatusPartialContent, 99000, size, fmt.Sprintf("bytes 99000-%d/%d", size-1, size)},
	} {
		resp, body, err := get(t, t.Context(), h.URL(), http.MethodGet, test.rangeHeader)
		if err != nil {
			t.Fatalf("%q: %v", test.rangeHeader, err)
		}
		if resp.StatusCode != test.status || !bytes.Equal(body, core.data[test.start:test.end]) ||
			resp.Header.Get("Accept-Ranges") != "bytes" || resp.ContentLength != test.end-test.start ||
			resp.Header.Get("Content-Range") != test.contentRange {
			t.Fatalf("%q: status %d, %d bytes, headers %v", test.rangeHeader, resp.StatusCode, len(body), resp.Header)
		}
	}
	resp, _, err := get(t, t.Context(), h.URL(), http.MethodGet, fmt.Sprintf("bytes=%d-", size))
	if err != nil || resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", size) {
		t.Fatalf("range past the end: %v %v", resp, err)
	}
	requests := len(core.seenRanges())
	resp, body, err := get(t, t.Context(), h.URL(), http.MethodHead, "bytes=10-19")
	if err != nil || resp.StatusCode != http.StatusOK || len(body) != 0 || resp.ContentLength != size || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD: %v %v", resp, err)
	}
	if got := len(core.seenRanges()); got != requests {
		t.Fatalf("HEAD reached the core (%d requests, want %d)", got, requests)
	}
	if h.Err() != nil {
		t.Fatalf("handle failed: %v", h.Err())
	}
}

func TestRelayResumesAfterTheCoreCutsTheBody(t *testing.T) {
	core := newFakeCore(t, 300_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	core.queue(injection{cutAfter: 1000}, injection{cutAfter: 50_000})
	resp, body, err := get(t, t.Context(), h.URL(), http.MethodGet, "")
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, core.data) {
		t.Fatalf("resumed read: status %v, %d bytes, %v", resp, len(body), err)
	}
	want := []string{"bytes=0-299999", "bytes=1000-299999", "bytes=51000-299999"}
	if got := core.seenRanges(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("upstream ranges = %v, want %v", got, want)
	}
}

func TestRelayRetriesTransientStatusesAndKeepsTheHandle(t *testing.T) {
	core := newFakeCore(t, 10_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	// 401 is transient too: the core reloads node keys on a signature it
	// cannot verify, so a key rotation in progress heals.
	core.queue(injection{status: 500}, injection{status: 502}, injection{status: 429}, injection{status: 503}, injection{status: 401})
	resp, body, err := get(t, t.Context(), h.URL(), http.MethodGet, "")
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, core.data) {
		t.Fatalf("read through transient failures: %v, %d bytes, %v", resp, len(body), err)
	}
	if got := len(core.seenRanges()); got != 6 {
		t.Fatalf("upstream requests = %d, want 6", got)
	}
	if h.Err() != nil {
		t.Fatalf("transient failures failed the handle: %v", h.Err())
	}
}

func TestRelayFatalStatusFailsTheHandle(t *testing.T) {
	// A malformed request (400) does not heal on a retry either.
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusPreconditionFailed} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			core := newFakeCore(t, 10_000)
			h := openRemote(t, newTestRelay(t, core, ModeCore))
			core.queue(injection{status: status})
			resp, _, err := get(t, t.Context(), h.URL(), http.MethodGet, "")
			if err != nil || resp.StatusCode != status {
				t.Fatalf("status %v, %v", resp, err)
			}
			var statusErr *StatusError
			if !errors.As(h.Err(), &statusErr) || statusErr.Status != status || h.Context().Err() == nil {
				t.Fatalf("handle err = %v", h.Err())
			}
			requests := len(core.seenRanges())
			resp, _, err = get(t, t.Context(), h.URL(), http.MethodGet, "")
			if err != nil || resp.StatusCode != status || len(core.seenRanges()) != requests {
				t.Fatalf("read of a failed handle: %v, %v, requests %d -> %d", resp, err, requests, len(core.seenRanges()))
			}
		})
	}
}

func TestRelayPassesARefusedRangeThrough(t *testing.T) {
	core := newFakeCore(t, 10_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	core.queue(injection{status: http.StatusRequestedRangeNotSatisfiable})
	resp, _, err := get(t, t.Context(), h.URL(), http.MethodGet, "bytes=5-")
	if err != nil || resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Range") != "bytes */10000" {
		t.Fatalf("416: %v %v", resp, err)
	}
	if h.Err() != nil {
		t.Fatalf("a 416 failed the handle: %v", h.Err())
	}
}

// An upstream that keeps failing mid-body fails that read, never the
// handle: the next read may succeed.
func TestRelayGivesUpOnAStalledUpstreamButKeepsTheHandle(t *testing.T) {
	core := newFakeCore(t, 10_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	failures := []injection{{cutAfter: 100}}
	for range 200 {
		failures = append(failures, injection{status: 500})
	}
	core.queue(failures...)
	if h.UpstreamFailed() {
		t.Fatal("a fresh handle reports an upstream failure")
	}
	// The relay aborts the response: FFmpeg sees a broken read, not a short
	// success, and the handle remembers that the core failed it.
	if resp, body, err := get(t, t.Context(), h.URL(), http.MethodGet, ""); err == nil {
		t.Fatalf("a stalled upstream completed: %v, %d bytes", resp, len(body))
	}
	if h.Err() != nil {
		t.Fatalf("a stalled upstream failed the handle: %v", h.Err())
	}
	if !h.UpstreamFailed() {
		t.Fatal("the handle does not report the upstream failure")
	}
	core.mu.Lock()
	core.inject = nil
	core.mu.Unlock()
	if resp, body, err := get(t, t.Context(), h.URL(), http.MethodGet, ""); err != nil || !bytes.Equal(body, core.data) {
		t.Fatalf("read after recovery: %v, %v", resp, err)
	}
}

// FFmpeg abandoning a read (a seek) ends the upstream request without
// touching the handle.
func TestRelayDownstreamCancelDoesNotFailTheHandle(t *testing.T) {
	core := newFakeCore(t, 100_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	core.queue(injection{cutAfter: 1000, stall: true})
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.URL(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = resp.Body.Close()
	waitSourceDone(t, core)
	if h.Err() != nil {
		t.Fatalf("a canceled read failed the handle: %v", h.Err())
	}
}

func TestHandleCloseInterruptsABlockedWrite(t *testing.T) {
	core := newFakeCore(t, 32<<20)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	resp, err := http.Get(h.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Read a little and stop, as an FFmpeg held by back pressure does: the
	// relay's writes fill the socket and block.
	if _, err := io.ReadFull(resp.Body, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	waitSourceDone(t, core)
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a closed handle's response ended cleanly")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("closing the handle did not end the response")
	}
	if !errors.Is(h.Err(), ErrClosed) {
		t.Fatalf("handle err = %v", h.Err())
	}
	if r, _, err := get(t, t.Context(), h.URL(), http.MethodGet, ""); err != nil || r.StatusCode != http.StatusNotFound {
		t.Fatalf("closed handle still served: %v %v", r, err)
	}
}

func waitSourceDone(t *testing.T, core *fakeCore) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		core.mu.Lock()
		inFlight := core.inFlight
		core.mu.Unlock()
		if inFlight == 0 {
			return
		}
		select {
		case <-core.sourceDone:
		case <-deadline:
			t.Fatal("upstream request did not end")
		}
	}
}

// FFmpeg reading through the relay and a Go consumer reading through the
// ReaderAt share one handle; neither a 5xx nor a cut body from the core
// closes it under the other.
func TestRelayAndReaderAtShareTheHandleThroughFailures(t *testing.T) {
	core := newFakeCore(t, 2<<20)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	var failures []injection
	for i := range 40 {
		switch i % 3 {
		case 0:
			failures = append(failures, injection{status: 500})
		case 1:
			failures = append(failures, injection{status: 502})
		default:
			failures = append(failures, injection{cutAfter: 4096})
		}
	}
	core.queue(failures...)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 3 {
		wg.Go(func() {
			resp, body, err := get(t, context.Background(), h.URL(), http.MethodGet, "")
			if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, core.data) {
				errs <- fmt.Errorf("relay read: %v, %d bytes: %w", resp, len(body), err)
			}
		})
	}
	wg.Go(func() {
		reader, err := h.ReaderAt(context.Background())
		if err != nil {
			errs <- err
			return
		}
		defer func() { _ = reader.Close() }()
		rng := rand.New(rand.NewPCG(1, 2))
		// Within the reader's budget of twice the source.
		for range 20 {
			off := rng.Int64N(int64(len(core.data)) - 150_000)
			n := 1 + rng.IntN(150_000)
			buf := make([]byte, n)
			if _, err := reader.ReadAt(buf, off); err != nil {
				errs <- fmt.Errorf("ReadAt(%d, %d): %w", off, n, err)
				return
			}
			if !bytes.Equal(buf, core.data[off:off+int64(n)]) {
				errs <- fmt.Errorf("ReadAt(%d, %d) returned other bytes", off, n)
				return
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if h.Err() != nil {
		t.Fatalf("handle failed: %v", h.Err())
	}
}

func TestOpenLocalRequiresTheApprovedRevision(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeLocal)
	h, err := relay.Open(t.Context(), "dispatched-grant")
	if err != nil {
		t.Fatal(err)
	}
	if h.Remote() || h.Input() != core.path || h.URL() != "" || h.Identity() != core.etag || h.Size() != 1000 {
		t.Fatalf("local handle %+v", h)
	}
	_ = h.Close()

	// The node's mount sees another revision than the core approved.
	if err := os.WriteFile(core.path, []byte("rewritten on the node"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Open(t.Context(), "dispatched-grant"); !errors.Is(err, ErrLocalMismatch) {
		t.Fatalf("mismatched local source: %v", err)
	}
}

func TestOpenRefusals(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeCore)
	if _, err := relay.Open(t.Context(), " "); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("no grant: %v", err)
	}
	core.queueOpens(http.StatusGone)
	var statusErr *StatusError
	if _, err := relay.Open(t.Context(), "g"); !errors.As(err, &statusErr) || !statusErr.Fatal() || statusErr.Status != http.StatusGone {
		t.Fatalf("refused open: %v", err)
	}
	core.queueOpens(http.StatusServiceUnavailable)
	if _, err := relay.Open(t.Context(), "g"); !errors.As(err, &statusErr) || statusErr.Fatal() {
		t.Fatalf("unavailable core: %v", err)
	}
}

func TestRenewalSwapsTheGrantAndKeepsTheRevision(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeCore)
	relay.renewInterval = 5 * time.Millisecond
	h := openRemote(t, relay)
	waitFor(t, func() bool { return core.openCount() >= 3 })
	if _, body, err := get(t, t.Context(), h.URL(), http.MethodGet, ""); err != nil || !bytes.Equal(body, core.data) {
		t.Fatal(err)
	}
	core.mu.Lock()
	used := core.grants[len(core.grants)-1]
	core.mu.Unlock()
	if used == "grant-1" || !strings.HasPrefix(used, "grant-") {
		t.Fatalf("source read with %q, want a renewed grant", used)
	}
	if h.Err() != nil {
		t.Fatal(h.Err())
	}
}

func TestRenewalRetriesTransientFailures(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeLocal)
	relay.renewInterval = 5 * time.Millisecond
	core.queueOpens(http.StatusOK, http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusBadGateway)
	h, err := relay.Open(t.Context(), "dispatched-grant")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	waitFor(t, func() bool { return core.openCount() >= 6 })
	if h.Err() != nil {
		t.Fatalf("transient renewal failures failed the handle: %v", h.Err())
	}
}

func TestRenewalRefusalFailsTheHandle(t *testing.T) {
	for name, setup := range map[string]func(*fakeCore){
		"gone":              func(c *fakeCore) { c.queueOpens(http.StatusOK, http.StatusGone) },
		"changed":           func(c *fakeCore) { c.queueOpens(http.StatusOK, http.StatusPreconditionFailed) },
		"another revision":  func(c *fakeCore) { c.openETag = `"file-1-1-1"` },
		"refused, retrying": func(c *fakeCore) { c.queueOpens(http.StatusOK, http.StatusInternalServerError, http.StatusForbidden) },
	} {
		t.Run(name, func(t *testing.T) {
			core := newFakeCore(t, 1000)
			setup(core)
			relay := newTestRelay(t, core, ModeCore)
			relay.renewInterval = 5 * time.Millisecond
			h := openRemote(t, relay)
			select {
			case <-h.Context().Done():
			case <-time.After(10 * time.Second):
				t.Fatal("a refused renewal left the handle open")
			}
			var statusErr *StatusError
			if !errors.As(h.Err(), &statusErr) || !statusErr.Fatal() {
				t.Fatalf("handle err = %v", h.Err())
			}
			if resp, _, err := get(t, t.Context(), h.URL(), http.MethodGet, ""); err != nil || resp.StatusCode != statusErr.Status {
				t.Fatalf("read after refusal: %v %v", resp, err)
			}
		})
	}
}

func TestRenewalGivesUpWhenTheGrantExpires(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeLocal)
	relay.renewInterval = 5 * time.Millisecond
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	relay.now = func() time.Time { return time.Unix(0, clock.Load()) }
	statuses := []int{http.StatusOK}
	for range 1000 {
		statuses = append(statuses, http.StatusServiceUnavailable)
	}
	core.queueOpens(statuses...)
	h, err := relay.Open(t.Context(), "dispatched-grant")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	waitFor(t, func() bool { return core.openCount() >= 3 })
	if h.Err() != nil {
		t.Fatalf("handle failed before its grant expired: %v", h.Err())
	}
	clock.Add(int64(2 * GrantLifetime))
	select {
	case <-h.Context().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("renewal kept retrying past the grant's expiry")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// A core that stops answering, before its answer, inside a refusal's body or
// mid-body, is abandoned after the stall timeout and the read resumes where
// it stopped: no upstream wait blocks a read forever.
func TestRelayResumesAStalledUpstream(t *testing.T) {
	for name, test := range map[string]struct {
		stalled injection
		resume  string
	}{
		"before the answer":       {injection{hang: true}, "bytes=0-99999"},
		"inside a refusal's body": {injection{status: http.StatusBadGateway, hang: true}, "bytes=0-99999"},
		"mid-body":                {injection{cutAfter: 1000, stall: true}, "bytes=1000-99999"},
	} {
		t.Run(name, func(t *testing.T) {
			core := newFakeCore(t, 100_000)
			relay := newTestRelay(t, core, ModeCore)
			relay.retry.stall, relay.retry.giveUp = 50*time.Millisecond, 10*time.Second
			h := openRemote(t, relay)
			core.queue(test.stalled)
			done := make(chan error, 1)
			var got []byte
			go func() {
				body, err := h.fetchRange(t.Context(), 0, int64(len(core.data)))
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = body.Close() }()
				got, err = io.ReadAll(body)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil || !bytes.Equal(got, core.data) {
					t.Fatalf("read through a stall: %d bytes, %v", len(got), err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a stalled upstream blocked the read")
			}
			if ranges := core.seenRanges(); len(ranges) != 2 || ranges[1] != test.resume {
				t.Fatalf("upstream ranges = %v, want a resume at %s", ranges, test.resume)
			}
			if h.Err() != nil {
				t.Fatalf("a stall failed the handle: %v", h.Err())
			}
		})
	}
}

// Time the caller spends between reads (FFmpeg's throttle, back pressure)
// does not count against the retry window, and a body left unread past the
// idle limit is replaced by a fresh request.
func TestRelayDownstreamPausesDoNotUseTheRetryWindow(t *testing.T) {
	newPausedRead := func(t *testing.T, size int) (*fakeCore, *Relay, *atomic.Int64) {
		t.Helper()
		core := newFakeCore(t, size)
		relay := newTestRelay(t, core, ModeCore)
		offset := &atomic.Int64{}
		relay.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
		return core, relay, offset
	}

	t.Run("a failure after a long pause is retried", func(t *testing.T) {
		core, relay, offset := newPausedRead(t, 100_000)
		h := openRemote(t, relay)
		core.queue(injection{cutAfter: 1000})
		body, err := h.fetchRange(t.Context(), 0, int64(len(core.data)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = body.Close() }()
		head := make([]byte, 1000)
		if _, err := io.ReadFull(body, head); err != nil {
			t.Fatal(err)
		}
		// The caller pauses far longer than the retry window, and the core
		// then fails the first attempt to resume.
		offset.Add(int64(time.Minute))
		core.queue(injection{status: http.StatusServiceUnavailable})
		rest, err := io.ReadAll(body)
		if err != nil || !bytes.Equal(append(head, rest...), core.data) {
			t.Fatalf("read after a pause: %d bytes, %v", len(rest), err)
		}
	})

	t.Run("an idle body is replaced by a fresh request", func(t *testing.T) {
		core, relay, offset := newPausedRead(t, 4<<20)
		h := openRemote(t, relay)
		body, err := h.fetchRange(t.Context(), 0, int64(len(core.data)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = body.Close() }()
		head := make([]byte, 1000)
		if _, err := io.ReadFull(body, head); err != nil {
			t.Fatal(err)
		}
		offset.Add(int64(relay.retry.idle + time.Second))
		next := make([]byte, 1000)
		if _, err := io.ReadFull(body, next); err != nil || !bytes.Equal(next, core.data[1000:2000]) {
			t.Fatalf("read after an idle pause: %v", err)
		}
		want := []string{"bytes=0-4194303", "bytes=1000-4194303"}
		if got := core.seenRanges(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("upstream ranges = %v, want %v", got, want)
		}
	})
}

// Closing the relay ends every handle, local or core access, stops their
// renewals, and refuses later opens without asking the core.
func TestRelayCloseEndsEveryHandle(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeLocal)
	relay.renewInterval = 5 * time.Millisecond
	local, err := relay.Open(t.Context(), "dispatched-grant")
	if err != nil {
		t.Fatal(err)
	}
	relay.mode = func() string { return ModeCore }
	remote := openRemote(t, relay)
	waitFor(t, func() bool { return core.openCount() >= 4 })
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]*Handle{"local": local, "remote": remote} {
		if !errors.Is(h.Err(), ErrRelayClosed) || h.Context().Err() == nil {
			t.Fatalf("%s handle after relay close: %v", name, h.Err())
		}
	}
	if lookup(remote.URL()) != nil {
		t.Fatal("a closed relay's URL still resolves")
	}
	opens := core.openCount()
	if _, err := relay.Open(t.Context(), "dispatched-grant"); !errors.Is(err, ErrRelayClosed) {
		t.Fatalf("open after close: %v", err)
	}
	// Close waited for the renewal loops, so none calls the core again.
	time.Sleep(20 * relay.renewInterval)
	if got := core.openCount(); got != opens {
		t.Fatalf("core called %d times after the relay closed", got-opens)
	}
}

// A loopback listen failure fails that open only; the next one listens.
func TestRelayRetriesALoopbackListenFailure(t *testing.T) {
	core := newFakeCore(t, 1000)
	relay := newTestRelay(t, core, ModeCore)
	listen := listenLoopback
	t.Cleanup(func() { listenLoopback = listen })
	listenLoopback = func() (net.Listener, error) { return nil, errors.New("no loopback port") }
	if _, err := relay.Open(t.Context(), "dispatched-grant"); err == nil {
		t.Fatal("open without a listener succeeded")
	}
	listenLoopback = listen
	h := openRemote(t, relay)
	if _, body, err := get(t, t.Context(), h.URL(), http.MethodGet, ""); err != nil || !bytes.Equal(body, core.data) {
		t.Fatalf("read after a listen retry: %v", err)
	}
}

// A node whose mount reports the modification time at another precision
// than the core's still opens the approved source locally.
func TestOpenLocalComparesTheRevisionToTheSecond(t *testing.T) {
	core := newFakeCore(t, 1000)
	approved := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	core.modTime = approved // the core's mount keeps whole seconds
	onNode := approved.Add(600 * time.Millisecond)
	if err := os.Chtimes(core.path, onNode, onNode); err != nil {
		t.Fatal(err)
	}
	core.etag = Revision{Kind: KindFile, ID: 1, Size: 1000, Mtime: approved.Unix()}.ETag()
	relay := newTestRelay(t, core, ModeLocal)
	h, err := relay.Open(t.Context(), "dispatched-grant")
	if err != nil {
		t.Fatalf("open at another precision: %v", err)
	}
	reader, err := h.ReaderAt(t.Context())
	if err != nil {
		t.Fatalf("local reader at another precision: %v", err)
	}
	_ = reader.Close()
	_ = h.Close()
	later := approved.Add(time.Second)
	if err := os.Chtimes(core.path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Open(t.Context(), "dispatched-grant"); !errors.Is(err, ErrLocalMismatch) {
		t.Fatalf("open a second later: %v", err)
	}
}

// TestReopenOpensASeparateHandle opens the source again from the handle's
// current grant: a handle of its own, which outlives the first one's close.
func TestReopenOpensASeparateHandle(t *testing.T) {
	core := newFakeCore(t, 5000)
	relay := newTestRelay(t, core, ModeCore)
	h := openRemote(t, relay)
	again, err := h.Reopen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if again == h || again.URL() == h.URL() || again.Identity() != h.Identity() || relay.openHandles() != 2 {
		t.Fatalf("reopened handle %q (%s), %d open", again.URL(), again.Identity(), relay.openHandles())
	}
	core.mu.Lock()
	reopenedWith := core.opens
	core.mu.Unlock()
	if reopenedWith != 2 {
		t.Fatalf("%d opens at the core, want 2", reopenedWith)
	}
	_ = h.Close()
	if !IsRemote(again.URL()) || again.Err() != nil || relay.openHandles() != 1 {
		t.Fatal("closing the first handle ended the reopened one")
	}
}
