package workerinput

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cancellationTransport func(*http.Request) (*http.Response, error)

func (f cancellationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type blockedSourceBody struct {
	ctx     context.Context
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
	first   bool
}

func (b *blockedSourceBody) Read(p []byte) (int, error) {
	if !b.first {
		b.first = true
		return copy(p, strings.Repeat("a", 16<<10)), nil
	}
	close(b.started)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *blockedSourceBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestCanceledClientReadPreservesRegisteredSourceForSeek(t *testing.T) {
	const size = 32 << 10
	blocked := make(chan *blockedSourceBody, 1)
	var calls atomic.Int32
	transport := cancellationTransport(func(r *http.Request) (*http.Response, error) {
		header := http.Header{"Etag": {`"revision"`}}
		if calls.Add(1) == 1 {
			body := &blockedSourceBody{ctx: r.Context(), started: make(chan struct{}), closed: make(chan struct{})}
			blocked <- body
			return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: size, Body: body}, nil
		}
		header.Set("Content-Range", "bytes 2-5/32768")
		return &http.Response{StatusCode: http.StatusPartialContent, Header: header, ContentLength: 4, Body: io.NopCloser(strings.NewReader("aaaa"))}, nil
	})
	adapter, err := New(Options{Origins: []string{"https://core.example"}, Allowed: func(string, uint64) bool { return true }, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	handle, err := adapter.Open(t.Context(), Source{ID: "source", Revision: "revision", URL: "https://core.example/source", Grant: "grant", ETag: `"revision"`, Size: size, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, handle.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	var body *blockedSourceBody
	select {
	case body = <-blocked:
	case <-time.After(time.Second):
		t.Fatal("upstream not opened")
	}
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("upstream read did not block")
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("canceled upstream read not released")
	}
	if !adapter.Allowed(handle.URL()) {
		t.Fatal("ordinary client cancellation retired source authority")
	}
	seek, err := http.NewRequest(http.MethodGet, handle.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seek.Header.Set("Range", "bytes=2-5")
	result, err := http.DefaultClient.Do(seek)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Body.Close() }()
	bytes, err := io.ReadAll(result.Body)
	if err != nil || result.StatusCode != http.StatusPartialContent || string(bytes) != "aaaa" {
		t.Fatalf("seek after cancel status=%d body=%q err=%v", result.StatusCode, bytes, err)
	}
}

func TestGenuineUpstreamShortBodyRetiresRegisteredSource(t *testing.T) {
	var calls atomic.Int32
	transport := cancellationTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"revision"`}}, ContentLength: 10, Body: io.NopCloser(strings.NewReader("abc"))}, nil
	})
	adapter, err := New(Options{Origins: []string{"https://core.example"}, Allowed: func(string, uint64) bool { return true }, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	handle, err := adapter.Open(t.Context(), Source{ID: "source", Revision: "revision", URL: "https://core.example/source", Grant: "grant", ETag: `"revision"`, Size: 10, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(handle.URL())
	if err == nil {
		_, err = io.ReadAll(response.Body)
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("short upstream body returned success")
	}
	if adapter.Allowed(handle.URL()) {
		t.Fatal("genuine truncated representation retained authority")
	}
	next, err := http.Get(handle.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Body.Close() }()
	body, err := io.ReadAll(next.Body)
	if err != nil || calls.Load() != 1 || len(body) == 0 || next.StatusCode == http.StatusOK {
		t.Fatalf("retired source still fetched bytes: calls=%d status=%d body=%q err=%v", calls.Load(), next.StatusCode, body, err)
	}
}
