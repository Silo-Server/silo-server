package nodesource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// bytesCore is a core's sources route over content: it answers single
// ranges with the revision's ETag, the way nodecontrol.HandleSource does,
// and lets a test break individual answers.
type bytesCore struct {
	t       *testing.T
	content []byte
	etag    string
	server  *httptest.Server
	// requests counts the reads the core received.
	requests atomic.Int64
	// next, when set, answers the next request instead of the normal answer;
	// it returns false to fall through to the normal answer.
	mu   sync.Mutex
	next []func(w http.ResponseWriter, r *http.Request) bool
	// status, when non-zero, answers every request with it.
	status atomic.Int64
	// grants records the grants requests carried.
	grants sync.Map
}

func newBytesCore(t *testing.T, content []byte) *bytesCore {
	t.Helper()
	c := &bytesCore{t: t, content: content,
		etag: Revision{Kind: KindFile, ID: 42, Size: int64(len(content)), ModTimeUnixNano: 1_700_000_000_123_456_789}.ETag()}
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.server.Close)
	return c
}

func (c *bytesCore) then(answers ...func(w http.ResponseWriter, r *http.Request) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next = append(c.next, answers...)
}

func (c *bytesCore) serve(w http.ResponseWriter, r *http.Request) {
	c.requests.Add(1)
	c.grants.Store(r.Header.Get(HeaderSourceGrant), true)
	if status := c.status.Load(); status != 0 {
		http.Error(w, "refused", int(status))
		return
	}
	c.mu.Lock()
	var next func(w http.ResponseWriter, r *http.Request) bool
	if len(c.next) > 0 {
		next, c.next = c.next[0], c.next[1:]
	}
	c.mu.Unlock()
	if next != nil && next(w, r) {
		return
	}
	if r.Header.Get("If-Match") != c.etag || r.Header.Get("Accept-Encoding") != "identity" {
		http.Error(w, "bad conditions", http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("ETag", c.etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(c.content))
}

// fetch is the opener's FetchFunc: ReadSource without the signature.
func (c *bytesCore) fetch(ctx context.Context, read ReadRequest) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.server.URL+PathSources, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderSourceGrant, read.Grant)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", read.Offset, read.Offset+read.Length-1))
	req.Header.Set("If-Match", read.ETag)
	req.Header.Set("Accept-Encoding", "identity")
	return c.server.Client().Do(req)
}

// open approves the revision for any grant, as sources/open does.
func (c *bytesCore) open(_ context.Context, grant string) (*OpenResponse, error) {
	return &OpenResponse{Grant: "renewed-" + grant, Path: "/library/movie.mkv", Size: int64(len(c.content)),
		ModTimeUnixNano: 1_700_000_000_123_456_789, ETag: c.etag}, nil
}

// fastRetry is a retry policy short enough for a test.
var fastRetry = backoff{initial: 5 * time.Millisecond, max: 20 * time.Millisecond, giveUp: 500 * time.Millisecond,
	stall: 100 * time.Millisecond, idle: 100 * time.Millisecond}

// coreOpener is an opener in core mode against c.
func coreOpener(t *testing.T, c *bytesCore) *Opener {
	t.Helper()
	o := quietOpener(c.open)
	o.EnableCoreAccess(CoreAccess{Fetch: c.fetch, Mode: func() string { return ModeCore }})
	o.retry = fastRetry
	t.Cleanup(func() { _ = o.Close() })
	return o
}

func testContent(n int) []byte {
	content := make([]byte, n)
	for i := range content {
		content[i] = byte(i*7 + i/251)
	}
	return content
}

// readAll reads the relay URL like FFmpeg does, optionally from a range.
func readAll(t *testing.T, url, rangeHeader string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, body
}

func TestCoreSourceServesFFmpegAndReaders(t *testing.T) {
	content := testContent(300_000)
	core := newBytesCore(t, content)
	opener := coreOpener(t, core)
	source, err := opener.Open(t.Context(), "dispatched")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	url := source.Input()
	if !IsRelayURL(url) || !fspath.IsRelayed(url) {
		t.Fatalf("input %q is not a relay URL", url)
	}
	if status, body := readAll(t, url, ""); status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("whole source: %d, %d bytes", status, len(body))
	}
	if status, body := readAll(t, url, "bytes=1000-1999"); status != http.StatusPartialContent || !bytes.Equal(body, content[1000:2000]) {
		t.Fatalf("range: %d", status)
	}
	if status, _ := readAll(t, url, "bytes=300000-"); status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("past the end: %d", status)
	}

	// fspath answers for the URL.
	info, err := fspath.Stat(url)
	if err != nil || info.Size() != int64(len(content)) || !info.Mode().IsRegular() || info.ModTime().UnixNano() != 1_700_000_000_123_456_789 {
		t.Fatalf("stat: %v %v", info, err)
	}
	if _, err := fspath.Open(url); !errors.Is(err, fspath.ErrRelayed) || strings.Contains(err.Error(), url) {
		t.Fatalf("an os.File open of a relay URL: %v", err)
	}
	reader, err := fspath.OpenReader(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	for _, tc := range []struct{ off, n int }{{0, 10}, {65530, 20}, {100_000, 200_000}, {299_990, 10}} {
		buf := make([]byte, tc.n)
		if n, err := reader.ReadAt(buf, int64(tc.off)); err != nil || n != tc.n || !bytes.Equal(buf, content[tc.off:tc.off+tc.n]) {
			t.Errorf("ReadAt(%d, %d) = %d %v", tc.off, tc.n, n, err)
		}
	}
	buf := make([]byte, 20)
	if n, err := reader.ReadAt(buf, 299_990); n != 10 || !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt past the end = %d %v, want 10 and EOF", n, err)
	}
}

// One URL per revision: jobs on the same revision share it and its renewal,
// it resolves while any holds it, and the same revision gets the same URL
// again later in the process.
func TestCoreSourcesShareOneURLPerRevision(t *testing.T) {
	core := newBytesCore(t, testContent(1000))
	opener := coreOpener(t, core)
	a, err := opener.Open(t.Context(), "job-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := opener.Open(t.Context(), "job-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Input() != b.Input() {
		t.Fatalf("two jobs on one revision got %q and %q", a.Input(), b.Input())
	}
	opener.mu.Lock()
	entries, leases := len(opener.entries), len(opener.leases)
	opener.mu.Unlock()
	if entries != 1 || leases != 1 {
		t.Fatalf("%d entries and %d leases for one revision, want one renewal", entries, leases)
	}
	url := a.Input()
	_ = a.Close()
	if a.Err() == nil || b.Err() != nil {
		t.Fatalf("closing one job: a %v, b %v", a.Err(), b.Err())
	}
	if status, _ := readAll(t, url, "bytes=0-9"); status != http.StatusPartialContent {
		t.Fatalf("URL after one of two released it: %d", status)
	}
	_ = b.Close()
	if status, _ := readAll(t, url, "bytes=0-9"); status != http.StatusNotFound {
		t.Fatalf("URL after both released it: %d, want 404", status)
	}
	if _, err := fspath.Stat(url); err == nil || strings.Contains(err.Error(), url) {
		t.Fatalf("stat of a released URL: %v", err)
	}
	c, err := opener.Open(t.Context(), "job-c")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if c.Input() != url {
		t.Fatalf("the revision came back at %q, want %q", c.Input(), url)
	}
}

// The mode is read at each open: a job opened after a change uses the new
// mode, an open one keeps its own.
func TestOpenerReadsTheModeAtEachOpen(t *testing.T) {
	path := writeSource(t, "movie.mkv", "movie")
	local := approvedCore(t, path)
	core := newBytesCore(t, []byte("movie"))
	var mode atomic.Value
	mode.Store(ModeLocal)
	opener := quietOpener(local.open)
	opener.EnableCoreAccess(CoreAccess{Fetch: core.fetch, Mode: func() string { s, _ := mode.Load().(string); return s }})
	t.Cleanup(func() { _ = opener.Close() })
	first, err := opener.Open(t.Context(), "a")
	if err != nil || first.Input() != path {
		t.Fatalf("local open: %v %v", first, err)
	}
	mode.Store(ModeCore)
	second, err := opener.Open(t.Context(), "b")
	if err != nil || !IsRelayURL(second.Input()) {
		t.Fatalf("core open: %v %v", second, err)
	}
	if first.Input() != path || first.Err() != nil {
		t.Fatal("the open local source changed mode")
	}
	_ = first.Close()
	_ = second.Close()
}

// A body the core cuts short is resumed from where it stopped.
func TestCoreReadResumesACutBody(t *testing.T) {
	content := testContent(200_000)
	core := newBytesCore(t, content)
	core.then(func(w http.ResponseWriter, r *http.Request) bool {
		// Promise the range, send part of it, and drop the connection.
		w.Header().Set("ETag", core.etag)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(content)-1, len(content)))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[:50_000])
		_ = http.NewResponseController(w).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return true
	})
	opener := coreOpener(t, core)
	source, err := opener.Open(t.Context(), "dispatched")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if status, body := readAll(t, source.Input(), ""); status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("cut body: %d, %d bytes", status, len(body))
	}
	if core.requests.Load() < 2 {
		t.Fatal("the cut body was not resumed with a second request")
	}
}

// A body that stops delivering is abandoned and resumed; a core that keeps
// answering nothing fails the read after the retry window, and the relay
// aborts FFmpeg's connection rather than ending the body cleanly.
func TestCoreReadStallsAndGivesUp(t *testing.T) {
	content := testContent(100_000)
	core := newBytesCore(t, content)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stall := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("ETag", core.etag)
		start, length, _, _ := parseRange(r.Header.Get("Range"), int64(len(content)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, len(content)))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : start+1000])
		_ = http.NewResponseController(w).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	}
	core.then(stall)
	opener := coreOpener(t, core)
	source, err := opener.Open(t.Context(), "dispatched")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if status, body := readAll(t, source.Input(), ""); status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("one stalled body: %d, %d bytes", status, len(body))
	}

	// Every answer stalls before a byte: the read fails within the window
	// and FFmpeg sees a short body.
	silent := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("ETag", core.etag)
		start, length, _, _ := parseRange(r.Header.Get("Range"), int64(len(content)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, len(content)))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		_ = http.NewResponseController(w).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	}
	for range 100 {
		core.then(silent)
	}
	started := time.Now()
	resp, err := http.Get(source.Input())
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr == nil || len(body) >= len(content) {
		t.Fatalf("a body that cannot complete ended cleanly with %d bytes", len(body))
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("gave up after %s", elapsed)
	}
	if source.Err() != nil {
		t.Fatalf("a transient failure ended the source: %v", source.Err())
	}
}

// A reader that pauses longer than the idle timeout gets a fresh request
// rather than waiting on a connection the core may have dropped.
func TestCoreReadDropsAnIdleBody(t *testing.T) {
	content := testContent(100_000)
	core := newBytesCore(t, content)
	opener := coreOpener(t, core)
	source, err := opener.Open(t.Context(), "dispatched")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	e := source.(*coreHandle).e
	body, err := e.fetchRange(t.Context(), 0, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	buf := make([]byte, 1000)
	if _, err := io.ReadFull(body, buf); err != nil {
		t.Fatal(err)
	}
	before := core.requests.Load()
	time.Sleep(fastRetry.idle + 50*time.Millisecond) //nolint:forbidigo // the idle timeout is a time the reader must really pause for
	rest, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(slices.Concat(buf, rest), content) {
		t.Fatalf("after idling: %v, %d bytes", err, len(rest))
	}
	if core.requests.Load() == before {
		t.Fatal("an idle body was not re-requested")
	}
}

// A fatal status fails the read and the source, and every job reading it; a
// 429 or 5xx is transient.
func TestCoreReadFatalAndTransientStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusPreconditionFailed} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			core := newBytesCore(t, testContent(1000))
			opener := coreOpener(t, core)
			a, _ := opener.Open(t.Context(), "a")
			b, _ := opener.Open(t.Context(), "b")
			defer func() { _ = a.Close(); _ = b.Close() }()
			core.status.Store(int64(status))
			reader, err := fspath.OpenReader(t.Context(), a.Input())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close() }()
			if _, err := reader.ReadAt(make([]byte, 10), 0); !errors.As(err, new(*fs.PathError)) {
				t.Fatalf("read: %v, want a path error", err)
			}
			for _, source := range []Source{a, b} {
				select {
				case <-source.Context().Done():
				case <-time.After(time.Second):
					t.Fatal("a fatal status did not end the source")
				}
				if statusErr, ok := errors.AsType[*StatusError](source.Err()); !ok || statusErr.Status != status {
					t.Fatalf("source error %v, want status %d", source.Err(), status)
				}
			}
		})
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusUnauthorized} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			content := testContent(1000)
			core := newBytesCore(t, content)
			for range 3 {
				core.then(func(w http.ResponseWriter, r *http.Request) bool { http.Error(w, "busy", status); return true })
			}
			opener := coreOpener(t, core)
			source, _ := opener.Open(t.Context(), "a")
			defer func() { _ = source.Close() }()
			if got, body := readAll(t, source.Input(), "bytes=0-99"); got != http.StatusPartialContent || !bytes.Equal(body, content[:100]) {
				t.Fatalf("after three %ds: %d", status, got)
			}
			if source.Err() != nil {
				t.Fatalf("a transient status ended the source: %v", source.Err())
			}
		})
	}
}

// Concurrent reads of one page share one upstream request; reads of
// different pages run at once rather than one after another.
func TestReaderDeduplicatesConcurrentPageFetches(t *testing.T) {
	content := testContent(10 * readerPageBytes)
	core := newBytesCore(t, content)
	gate := make(chan struct{})
	var inFlight, peak atomic.Int64
	slow := func(w http.ResponseWriter, r *http.Request) bool {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-gate
		inFlight.Add(-1)
		return false
	}
	for range 20 {
		core.then(slow)
	}
	opener := coreOpener(t, core)
	opener.retry.stall = 5 * time.Second
	source, _ := opener.Open(t.Context(), "a")
	defer func() { _ = source.Close() }()
	reader, err := fspath.OpenReader(t.Context(), source.Input())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		wg.Go(func() {
			// 32 readers of page 0, 32 spread over pages 1 to 4.
			off := int64(i % 100)
			if i >= 32 {
				off = int64(1+i%4)*readerPageBytes + int64(i)
			}
			buf := make([]byte, 100)
			if _, err := reader.ReadAt(buf, off); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(buf, content[off:off+100]) {
				errs <- fmt.Errorf("wrong bytes at %d", off)
			}
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for peak.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls the fetches in flight, the observable state under test
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := core.requests.Load(); got != 5 {
		t.Errorf("%d upstream requests for 5 pages, want 5", got)
	}
	if peak.Load() != 5 {
		t.Errorf("%d fetches ran at once, want all 5 pages fetched concurrently", peak.Load())
	}
	// Cached now: no more requests.
	if _, err := reader.ReadAt(make([]byte, 100), 10); err != nil || core.requests.Load() != 5 {
		t.Errorf("a cached page was fetched again: %v, %d requests", err, core.requests.Load())
	}
}

// stalledResponse is a relay response whose reader stopped reading: it takes
// one write, then holds the next until a write deadline is set, as a socket
// under back pressure does.
type stalledResponse struct {
	header      http.Header
	writes      atomic.Int32
	held        chan struct{}
	holdOnce    sync.Once
	deadline    chan struct{}
	deadlineSet sync.Once
}

func (w *stalledResponse) Header() http.Header { return w.header }
func (w *stalledResponse) WriteHeader(int)     {}
func (w *stalledResponse) Flush()              {}

func (w *stalledResponse) Write(p []byte) (int, error) {
	if w.writes.Add(1) == 1 {
		return len(p), nil
	}
	w.holdOnce.Do(func() { close(w.held) })
	<-w.deadline
	return 0, os.ErrDeadlineExceeded
}

func (w *stalledResponse) SetWriteDeadline(time.Time) error {
	w.deadlineSet.Do(func() { close(w.deadline) })
	return nil
}

// Closing the last source interrupts a relay response FFmpeg has stopped
// reading, which back pressure holds in a write.
func TestCloseInterruptsABlockedRelayRead(t *testing.T) {
	core := newBytesCore(t, testContent(1<<20))
	opener := coreOpener(t, core)
	source, err := opener.Open(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	response := &stalledResponse{header: http.Header{}, held: make(chan struct{}), deadline: make(chan struct{})}
	served := make(chan struct{})
	var aborted atomic.Bool
	go func() {
		defer close(served)
		defer func() {
			if r := recover(); r != nil {
				if r != http.ErrAbortHandler { //nolint:errorlint // the sentinel a handler panics with
					panic(r)
				}
				aborted.Store(true)
			}
		}()
		opener.serve(response, httptest.NewRequest(http.MethodGet, source.Input(), nil))
	}()
	select {
	case <-response.held:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never reached a second write")
	}
	_ = source.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the source did not interrupt the blocked response")
	}
	if !aborted.Load() {
		t.Fatal("the cut response was finished as if it were whole")
	}
}

// A failed source read is never mistaken for the end of the source: parsers
// stop quietly on io.EOF and io.ErrUnexpectedEOF.
func TestAFailedSourceReadIsNotEndOfFile(t *testing.T) {
	entry := &relayEntry{lease: &lease{etag: `"file-42-5-1"`}}
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF, fmt.Errorf("fetch page: %w", io.ErrUnexpectedEOF)} {
		err := entry.readError(cause)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("read failure %q reads as end of file", err)
		}
		if _, ok := errors.AsType[*fs.PathError](err); !ok || !strings.Contains(err.Error(), cause.Error()) {
			t.Errorf("read failure %q lost its path error or its cause", err)
		}
	}
	refused := &StatusError{Op: "read", Status: http.StatusGone}
	if got, ok := errors.AsType[*StatusError](entry.readError(refused)); !ok || got != refused {
		t.Error("another read failure lost its type")
	}
}

func TestGuardRestrictsRelayedInputs(t *testing.T) {
	url := "http://127.0.0.1:41234/src/0123456789abcdef0123456789abcdef"
	restrictions := []string{"-protocol_whitelist", "http,tcp", "-format_whitelist", AdmittedFormats, "-http_proxy", ""}
	got := restrict([]string{"-hide_banner", "-ss", "10", "-i", url, "-c", "copy", "out.mp4"}, false)
	want := append(append([]string{"-hide_banner", "-ss", "10"}, restrictions...), "-i", url, "-c", "copy", "out.mp4")
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ffmpeg argv\n%v\nwant\n%v", got, want)
	}
	probe := restrict([]string{"-v", "error", "-show_streams", url}, true)
	if strings.Join(probe, " ") != strings.Join(append(append([]string{"-v", "error", "-show_streams"}, restrictions...), url), " ") {
		t.Fatalf("ffprobe argv %v", probe)
	}
	local := []string{"-i", "/library/movie.mkv", "out.mp4"}
	if out := restrict(local, false); &out[0] != &local[0] {
		t.Fatal("a local input was rewritten")
	}
	for _, format := range []string{"matroska", "mov", "m4v", "mpegts"} {
		if !strings.Contains(","+AdmittedFormats+",", ","+format+",") {
			t.Errorf("admitted formats %q leave out %s", AdmittedFormats, format)
		}
	}
	// fspath applies it to every launch, choosing the probe form for
	// ffprobe.
	// The argv is compared element by element: the empty -http_proxy value
	// does not survive a join.
	cmd := fspath.CommandContext(t.Context(), "/usr/bin/ffprobe", "-v", "error", url)
	if !slices.Equal(cmd.Args[1:], append(append([]string{"-v", "error"}, restrictions...), url)) {
		t.Fatalf("fspath launch of ffprobe: %q", cmd.Args)
	}
	cmd = fspath.CommandContext(t.Context(), "/usr/bin/ffmpeg", "-i", url, "-f", "null", "-")
	if !slices.Equal(cmd.Args[1:], append(slices.Clone(restrictions), "-i", url, "-f", "null", "-")) {
		t.Fatalf("fspath launch of ffmpeg: %q", cmd.Args)
	}
	// A relayed launch runs without the proxy variables; a local one keeps
	// the environment it inherits.
	t.Setenv("http_proxy", "http://proxy.invalid:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	if env := fspath.CommandContext(t.Context(), "/usr/bin/ffmpeg", "-i", url).Env; env == nil ||
		slices.ContainsFunc(env, func(kv string) bool { return strings.Contains(kv, "proxy.invalid") }) {
		t.Fatal("a relayed launch kept the proxy variables")
	}
	if env := fspath.CommandContext(t.Context(), "/usr/bin/ffmpeg", "-i", "/library/movie.mkv").Env; env != nil {
		t.Fatal("a local launch got a rewritten environment")
	}
}

func TestRedactionHidesRelayURLs(t *testing.T) {
	core := newBytesCore(t, testContent(100))
	opener := coreOpener(t, core)
	source, _ := opener.Open(t.Context(), "a")
	url := source.Input()
	if got := RedactRelayURLs("Input #0, matroska, from '" + url + "':"); got != "Input #0, matroska, from 'source "+core.etag+"':" {
		t.Fatalf("redacted %q", got)
	}
	var buf bytes.Buffer
	logger := slogTextLogger(&buf)
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "ffmpeg cmd "+url, 0)
	record.Add("input", url, "args", []string{"-i", url}, "error", &os.PathError{Op: "open", Path: url, Err: os.ErrNotExist})
	if err := logger.Handler().Handle(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	logger.With("input", url).Info("with attrs")
	_ = source.Close()
	logger.Info("after release", "input", url)
	if strings.Contains(buf.String(), "/src/") {
		t.Fatalf("a relay URL reached the log:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "source (released)") || !strings.Contains(buf.String(), strings.Trim(core.etag, `"`)) {
		t.Fatalf("log does not name the source:\n%s", buf.String())
	}
}

func slogTextLogger(w io.Writer) *slog.Logger {
	return slog.New(RedactingHandler(slog.NewTextHandler(w, nil)))
}
