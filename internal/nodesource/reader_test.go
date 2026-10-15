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
	"os"
	"strings"
	"testing"
	"time"
)

func TestReaderAtCachesPagesAndFetchesLargeReadsDirectly(t *testing.T) {
	core := newFakeCore(t, 1<<20)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	reader, err := h.ReaderAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	read := func(off int64, n int) {
		t.Helper()
		buf := make([]byte, n)
		if _, err := reader.ReadAt(buf, off); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf, core.data[off:off+int64(n)]) {
			t.Fatalf("ReadAt(%d, %d) returned other bytes", off, n)
		}
	}
	read(0, 10)
	read(100, 10)
	read(65_000, 500) // the first page, cached
	if got := core.seenRanges(); len(got) != 1 || got[0] != "bytes=0-65535" {
		t.Fatalf("small reads fetched %v, want one page", got)
	}
	read(70_000, 10)
	read(200_000, 300_000)
	got := core.seenRanges()
	if len(got) != 3 || got[1] != "bytes=65536-131071" || got[2] != "bytes=200000-499999" {
		t.Fatalf("fetches = %v", got)
	}
	// The tail reads short with io.EOF.
	buf := make([]byte, 100)
	if n, err := reader.ReadAt(buf, int64(len(core.data))-40); n != 40 || !errors.Is(err, io.EOF) {
		t.Fatalf("tail read = %d, %v", n, err)
	}
}

func TestReaderAtStopsAtItsBudget(t *testing.T) {
	core := newFakeCore(t, 200_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	reader, err := h.ReaderAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	// Direct reads are not cached, so each fetches again; the budget is
	// twice the source.
	buf := make([]byte, 150_000)
	for i := range 2 {
		if _, err := reader.ReadAt(buf, 0); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	_, err = reader.ReadAt(buf, 0)
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || !errors.Is(err, ErrReaderLimit) {
		t.Fatalf("over budget: %v", err)
	}
	if h.Err() != nil {
		t.Fatalf("an exhausted budget failed the handle: %v", h.Err())
	}
}

func TestReaderAtErrorsArePathErrors(t *testing.T) {
	core := newFakeCore(t, 100_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	reader, err := h.ReaderAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	core.queue(injection{status: http.StatusGone})
	_, err = reader.ReadAt(make([]byte, 10), 0)
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || strings.Contains(pathErr.Path, "127.0.0.1") {
		t.Fatalf("withdrawn source: %v", err)
	}
	if h.Err() == nil {
		t.Fatal("a fatal answer left the handle open")
	}
	_ = reader.Close()

	core = newFakeCore(t, 100_000)
	h = openRemote(t, newTestRelay(t, core, ModeCore))
	ctx, cancel := context.WithCancel(t.Context())
	reader, err = h.ReaderAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := reader.ReadAt(make([]byte, 10), 0); !errors.As(err, &pathErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if h.Err() != nil {
		t.Fatalf("a canceled read failed the handle: %v", h.Err())
	}
	_ = reader.Close()
	if _, err := reader.ReadAt(make([]byte, 10), 0); !errors.As(err, &pathErr) {
		t.Fatalf("read after close: %v", err)
	}
}

// TestReaderAtWaitsForAFreeSlot queues a reader opened while every slot is
// taken until one frees, and gives up only when its caller does.
func TestReaderAtWaitsForAFreeSlot(t *testing.T) {
	core := newFakeCore(t, 1000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	var readers []Reader
	defer func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}()
	for range cap(readerSlots) {
		r, err := h.ReaderAt(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, r)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.ReaderAt(canceled); !errors.Is(err, context.Canceled) || !errors.As(err, new(*fs.PathError)) {
		t.Fatalf("reader for a gone caller: %v", err)
	}
	type opened struct {
		r   Reader
		err error
	}
	waiting := make(chan opened, 1)
	go func() {
		r, err := h.ReaderAt(t.Context())
		waiting <- opened{r, err}
	}()
	select {
	case got := <-waiting:
		t.Fatalf("reader past the limit did not wait: %v", got.err)
	case <-time.After(50 * time.Millisecond):
	}
	_ = readers[0].Close()
	got := <-waiting
	if got.err != nil {
		t.Fatalf("reader after one closed: %v", got.err)
	}
	readers[0] = got.r
}

func TestRegistryDescribesLocalPathsAndRelayedSources(t *testing.T) {
	core := newFakeCore(t, 5000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))

	info, err := Stat(h.URL())
	if err != nil || info.Size() != 5000 || !info.Mode().IsRegular() || info.ModTime().Unix() != core.modTime.Unix() || info.ModTime().Nanosecond() != 0 {
		t.Fatalf("Stat(relayed) = %v, %v", info, err)
	}
	if id, err := Identity(h.URL()); err != nil || id != core.etag {
		t.Fatalf("Identity(relayed) = %q, %v", id, err)
	}
	reader, err := OpenReaderAt(t.Context(), h.URL())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	if _, err := reader.ReadAt(buf, 4000); err != nil || !bytes.Equal(buf, core.data[4000:4100]) {
		t.Fatalf("relayed ReadAt: %v", err)
	}
	_ = reader.Close()

	local, err := os.Stat(core.path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := Stat(core.path); err != nil || info.Size() != local.Size() {
		t.Fatalf("Stat(local) = %v, %v", info, err)
	}
	if id, err := Identity(core.path); err != nil || !strings.HasPrefix(id, core.path+"|5000|") {
		t.Fatalf("Identity(local) = %q, %v", id, err)
	}
	reader, err = OpenReaderAt(t.Context(), core.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadAt(buf, 0); err != nil || !bytes.Equal(buf, core.data[:100]) {
		t.Fatalf("local ReadAt: %v", err)
	}
	_ = reader.Close()

	url := h.URL()
	_ = h.Close()
	if _, err := Stat(url); err == nil {
		t.Fatal("a released URL still describes its source")
	}
}

func TestRedactLoopbackHidesRelayURLs(t *testing.T) {
	core := newFakeCore(t, 100)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	line := "Input #0, matroska, from '" + h.URL() + "':"
	got := RedactLoopback(line)
	if strings.Contains(got, h.URL()) || !strings.Contains(got, core.etag) {
		t.Fatalf("redacted = %q", got)
	}
	url := h.URL()
	_ = h.Close()
	if got := RedactLoopback("-i " + url); strings.Contains(got, url) || !strings.Contains(got, "released") {
		t.Fatalf("redacted after release = %q", got)
	}
	if got := RedactLoopback("/media/movie.mkv"); got != "/media/movie.mkv" {
		t.Fatalf("a path changed: %q", got)
	}

	var logged bytes.Buffer
	logger := slog.New(RedactingHandler(slog.NewTextHandler(&logged, nil)))
	logger.With("input", url).Info("source opened", "detail", "open "+url, "error", errors.New("open "+url+": no such file"), slog.Group("ffmpeg", "line", url))
	if strings.Contains(logged.String(), url) {
		t.Fatalf("log carries the relay URL: %s", logged.String())
	}
}

// A cached page is not served once the reader's context ends or the source
// is withdrawn; the failure is the *fs.PathError every read failure is.
func TestReaderAtServesNoCachedPageAfterItsSourceEnds(t *testing.T) {
	core := newFakeCore(t, 200_000)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	buf := make([]byte, 10)

	ctx, cancel := context.WithCancel(t.Context())
	reader, err := h.ReaderAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := reader.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	var pathErr *fs.PathError
	if _, err := reader.ReadAt(buf, 0); !errors.As(err, &pathErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cached read after the reader's context ended: %v", err)
	}

	withdrawn, err := h.ReaderAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = withdrawn.Close() }()
	if _, err := withdrawn.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	h.fail(&StatusError{Op: opRead, Status: http.StatusGone})
	var statusErr *StatusError
	if _, err := withdrawn.ReadAt(buf, 0); !errors.As(err, &pathErr) || !errors.As(err, &statusErr) || statusErr.Status != http.StatusGone {
		t.Fatalf("cached read after the source was withdrawn: %v", err)
	}
}

// TestSequentialReaderStreamsAPass reads a source front to back the way a
// keyframe index check does, a few hundred bytes every 40 KB, through a core
// that takes 25 ms to answer each request. Page by page that is 400 requests,
// ten seconds; the sequential reader streams it in one request well within
// the test's two-second limit, and a read behind its stream starts another.
func TestSequentialReaderStreamsAPass(t *testing.T) {
	const size = 16 << 20
	core := newFakeCore(t, size)
	core.latency = 25 * time.Millisecond
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	reader, err := h.SequentialReaderAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	buf := make([]byte, 300)
	for off := int64(0); off < size; off += 40_000 {
		n, err := reader.ReadAt(buf, off)
		if err != nil && (!errors.Is(err, io.EOF) || off+int64(n) != size) {
			t.Fatalf("ReadAt(%d) = %d, %v", off, n, err)
		}
		if !bytes.Equal(buf[:n], core.data[off:off+int64(n)]) {
			t.Fatalf("ReadAt(%d) returned other bytes", off)
		}
	}
	if got := core.seenRanges(); len(got) != 1 || got[0] != fmt.Sprintf("bytes=0-%d", size-1) {
		t.Fatalf("a pass fetched %v, want one streamed range", got)
	}
	// Behind the stream, beyond the cache: a new request from there.
	if _, err := reader.ReadAt(buf, 1000); err != nil || !bytes.Equal(buf, core.data[1000:1300]) {
		t.Fatalf("read behind the stream: %v", err)
	}
	if got := core.seenRanges(); len(got) != 2 || got[1] != fmt.Sprintf("bytes=0-%d", size-1) {
		t.Fatalf("read behind the stream fetched %v", got)
	}
}

// TestSequentialReaderKeepsTheBudget stops a sequential reader at twice the
// source, counting what it streams: three passes over a source larger than
// the page cache read it three times.
func TestSequentialReaderKeepsTheBudget(t *testing.T) {
	const size = 3 << 20
	core := newFakeCore(t, size)
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	reader, err := h.SequentialReaderAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	buf := make([]byte, 256<<10)
	pass := func() error {
		for off := int64(0); off < size; off += int64(len(buf)) {
			if _, err := reader.ReadAt(buf, off); err != nil {
				return err
			}
		}
		return nil
	}
	for i := range 2 {
		if err := pass(); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if err := pass(); !errors.Is(err, ErrReaderLimit) || !errors.As(err, new(*fs.PathError)) {
		t.Fatalf("past the budget: %v", err)
	}
	if h.Err() != nil {
		t.Fatalf("an exhausted budget failed the handle: %v", h.Err())
	}
}

func TestSequentialTimeoutGrowsWithTheSource(t *testing.T) {
	for _, tc := range []struct {
		size int64
		want time.Duration
	}{
		{0, readerTimeout},
		{4 << 30, readerTimeout + 256*time.Second},
		{1 << 40, sequentialMaxTimeout},
	} {
		if got := sequentialTimeout(tc.size); got != tc.want {
			t.Errorf("sequentialTimeout(%d) = %s, want %s", tc.size, got, tc.want)
		}
	}
}

// TestSequentialReaderCloseInterruptsAStalledRead closes a sequential reader
// while a read waits on a stream the core stopped sending: Close ends the
// read rather than waiting behind it for the stall timeout.
func TestSequentialReaderCloseInterruptsAStalledRead(t *testing.T) {
	core := newFakeCore(t, 1<<20)
	core.queue(injection{cutAfter: 1000, stall: true})
	h := openRemote(t, newTestRelay(t, core, ModeCore))
	reader, err := h.SequentialReaderAt(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() {
		_, err := reader.ReadAt(make([]byte, 300), 2000)
		read <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(core.seenRanges()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the read never reached the core")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() {
		_ = reader.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited behind the stalled read")
	}
	if err := <-read; err == nil {
		t.Fatal("the stalled read succeeded after Close")
	}
}
