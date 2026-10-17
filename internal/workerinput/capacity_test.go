package workerinput

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestConfiguredInputServes33ConcurrentSources(t *testing.T) {
	const readers = 33
	entered := make(chan struct{}, readers)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"revision"`)
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(origin.Close)
	adapter, err := New(Options{Origins: []string{origin.URL}, AllowHTTP: true, MaxReads: readers, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	handle, err := adapter.Open(ctx, Source{ID: "source", Revision: "revision", URL: origin.URL + "/source", Grant: "grant", ETag: `"revision"`, Size: 4, WorkID: "work", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	var handlers sync.WaitGroup
	for range readers {
		handlers.Go(func() {
			adapter.serve(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, handle.URL(), nil))
		})
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for admitted := 0; admitted < readers; admitted++ {
		select {
		case <-entered:
		case <-deadline.C:
			t.Fatalf("configured input admitted only %d/%d sources", admitted, readers)
		}
	}
	response := httptest.NewRecorder()
	adapter.serve(response, httptest.NewRequestWithContext(ctx, http.MethodGet, handle.URL(), nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("configured input exceeded resource bound: status=%d", response.Code)
	}
	cancel()
	done := make(chan struct{})
	go func() { handlers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation retained source readers")
	}
	if len(adapter.reads) != 0 {
		t.Fatalf("cancellation retained %d source slots", len(adapter.reads))
	}
}
