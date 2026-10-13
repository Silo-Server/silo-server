package workerinput

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthorizedRangeAndRevocation(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	var upstreamCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Header.Get("X-Silo-Source-Grant") != "opaque" || r.Header.Get("If-Match") != `"revision"` || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("missing scoped source headers")
		}
		w.Header().Set("ETag", `"revision"`)
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "2345")
	}))
	defer origin.Close()
	adapter, err := New(Options{AllowHTTP: true, Origins: []string{origin.URL}, Allowed: func(string, uint64) bool { return active.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	handle, err := adapter.Open(context.Background(), Source{ID: "media-1", Revision: "r1", URL: origin.URL + "/source", Grant: "opaque", ETag: `"revision"`, Size: 10, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	req, _ := http.NewRequest(http.MethodGet, handle.URL(), nil)
	req.Header.Set("Range", "bytes=2-5")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != 206 || string(body) != "2345" {
		t.Fatalf("range: status=%d body=%q err=%v", res.StatusCode, body, err)
	}
	active.Store(false)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden || upstreamCalls.Load() != 1 {
		t.Fatal("revocation still reaches upstream")
	}
}

func TestRejectsOriginEscapeAndBadRangeResponse(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"revision"`)
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer origin.Close()
	adapter, err := New(Options{AllowHTTP: true, Origins: []string{origin.URL}, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	_, err = adapter.Open(t.Context(), Source{ID: "escape", Revision: "r", URL: "http://example.invalid/source", Size: 10, WorkID: "work", Generation: 1})
	if err == nil {
		t.Fatal("unapproved origin accepted")
	}
	handle, err := adapter.Open(t.Context(), Source{ID: "media", Revision: "r", URL: origin.URL + "/source", Grant: "g", ETag: `"revision"`, Size: 10, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	req, _ := http.NewRequest(http.MethodGet, handle.URL(), nil)
	req.Header.Set("Range", "bytes=1-2")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadGateway || strings.Contains(string(body), "012345") {
		t.Fatal("unexpected full source escaped range")
	}
}

func TestNoRedirectOrTLSBypass(t *testing.T) {
	var escaped atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Store(true) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer origin.Close()
	adapter, err := New(Options{AllowHTTP: true, Origins: []string{origin.URL}, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	handle, err := adapter.Open(t.Context(), Source{ID: "media", Revision: "r", URL: origin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	res, err := http.Get(handle.URL())
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if escaped.Load() || res.StatusCode != http.StatusBadGateway {
		t.Fatal("followed source redirect")
	}
	tlsOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Store(true) }))
	defer tlsOrigin.Close()
	tlsAdapter, err := New(Options{Origins: []string{tlsOrigin.URL}, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tlsAdapter.Close() }()
	tlsHandle, err := tlsAdapter.Open(t.Context(), Source{ID: "media", Revision: "r", URL: tlsOrigin.URL, Grant: "g", ETag: `"r"`, Size: 10, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tlsHandle.Close() }()
	res, err = http.Get(tlsHandle.URL())
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if escaped.Load() || res.StatusCode != http.StatusBadGateway {
		t.Fatal("accepted unknown TLS authority")
	}
}
