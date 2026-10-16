package workerinput

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClosedHandleReleasesBackpressuredRead(t *testing.T) {
	const size = 32 << 20
	coreFinished := make(chan struct{})
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(coreFinished)
		w.Header().Set("ETag", `"revision"`)
		w.Header().Set("Content-Length", strconv.Itoa(size))
		block := []byte(strings.Repeat("a", 64<<10))
		for sent := 0; sent < size; sent += len(block) {
			if _, err := w.Write(block); err != nil {
				return
			}
		}
	}))
	t.Cleanup(core.Close)
	adapter, err := New(Options{AllowHTTP: true, Origins: []string{core.URL}, MaxReads: 1, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	closed := make(chan struct{})
	loopback := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		adapter.serve(w, r)
	}))
	loopback.Config.ConnContext = func(ctx context.Context, connection net.Conn) context.Context {
		if tcp, ok := connection.(*net.TCPConn); ok {
			_ = tcp.SetWriteBuffer(1024)
		}
		return ctx
	}
	loopback.Start()
	t.Cleanup(loopback.Close)
	handle, err := adapter.Open(t.Context(), Source{ID: "source", Revision: "revision", URL: core.URL + "/source", Grant: "grant", ETag: `"revision"`, Size: size, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(loopback.URL + "/input/" + handle.token)
	if err != nil {
		t.Fatal(err)
	}
	address, err := net.ResolveTCPAddr("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTCP("tcp", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(connection, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", parsed.Path, parsed.Host); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(); _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK || len(adapter.reads) != 1 {
		t.Fatal("paused read did not occupy its source-reader quota")
	}
	// Intentionally hold the healthy downstream body unread so its bounded
	// TCP windows fill before the handle is revoked.
	pause := time.NewTimer(200 * time.Millisecond)
	defer pause.Stop()
	<-pause.C
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatalf("closed handle kept a blocked handler: active readers%d", len(adapter.reads))
	}
	if len(adapter.reads) != 0 {
		t.Fatal("closed handle retained source-reader capacity")
	}
	select {
	case <-coreFinished:
	case <-time.After(time.Second):
		t.Fatal("closed handle retained upstream source reader")
	}
	count, err := io.Copy(io.Discard, response.Body)
	if err == nil || count >= size {
		t.Fatalf("closed handle delivered its full body: bytes%d err%v", count, err)
	}
}
