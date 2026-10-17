package workerinput

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBusyLoopbackInputReturnsRetryable503(t *testing.T) {
	entered := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"revision"`)
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	t.Cleanup(origin.Close)
	adapter, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, MaxReads: 1, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	handle, err := adapter.Open(t.Context(), Source{ID: "source", Revision: "revision", URL: origin.URL + "/source", Grant: "grant", ETag: `"revision"`, Size: 4, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	t.Cleanup(cancelFirst)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		adapter.serve(httptest.NewRecorder(), httptest.NewRequestWithContext(firstCtx, http.MethodGet, handle.URL(), nil))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first source read did not acquire capacity")
	}
	secondCtx, cancelSecond := context.WithTimeout(t.Context(), time.Second)
	defer cancelSecond()
	response := httptest.NewRecorder()
	adapter.serve(response, httptest.NewRequestWithContext(secondCtx, http.MethodGet, handle.URL(), nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("busy input must return retryable 503: status=%d headers=%v", response.Code, response.Header())
	}
	if secondCtx.Err() != nil {
		t.Fatal("busy input waited for caller cancellation")
	}
	if err := handle.Validate(t.Context()); err != nil {
		t.Fatalf("resource contention revoked admitted input: %v", err)
	}
	cancelFirst()
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled source read retained capacity")
	}
}

func TestInputRejectsInvalidResourceLimits(t *testing.T) {
	for _, options := range []Options{{MaxReads: -1}, {MaxReads: 4097}, {MaxInputs: -1}, {MaxInputs: 65537}} {
		options.Origins = []string{"http://core.example.test"}
		options.AllowHTTP = true
		options.Allowed = func(string, uint64) bool { return true }
		adapter, err := New(options)
		if adapter != nil {
			_ = adapter.Close()
		}
		if err == nil {
			t.Errorf("invalid source limit admitted: reads=%d inputs=%d", options.MaxReads, options.MaxInputs)
		}
	}
}
