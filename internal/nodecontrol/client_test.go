package nodecontrol

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/opslog"
)

// The node's client and the core's routes agree end to end: every call is
// signed so RequireNode accepts it, mounted under the core URL's path.
func TestClientAndCoreAgree(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	mux := http.NewServeMux()
	mux.Handle("/silo/", http.StripPrefix("/silo", f.handler))
	core := httptest.NewServer(mux)
	t.Cleanup(core.Close)
	client, err := NewClient(core.URL+"/silo/", key)
	if err != nil {
		t.Fatal(err)
	}
	if client.NodeID() != 7 {
		t.Fatalf("NodeID = %d, want the key's 7", client.NodeID())
	}
	ctx := t.Context()

	doc, notModified, err := client.FetchConfig(ctx, "")
	if err != nil || notModified || doc.Node.ID != 7 || doc.Playback.TrickplayNodeCapacity != 4 {
		t.Fatalf("FetchConfig = %+v, %v, %v", doc, notModified, err)
	}
	if again, notModified, err := client.FetchConfig(ctx, `"`+doc.Revision+`"`); err != nil || !notModified || again != nil {
		t.Fatalf("FetchConfig with the current revision = %+v, %v, %v; want not modified", again, notModified, err)
	}

	body, err := json.Marshal(testReport("1800000000000000000", 1, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PostReport(ctx, body); err != nil {
		t.Fatalf("PostReport: %v", err)
	}
	if err := client.PostReport(ctx, body); Status(err) != http.StatusConflict {
		t.Fatalf("replayed report: %v, want a 409 StatusError", err)
	}
	if err := client.ShipLogs(ctx, []opslog.Entry{{Timestamp: time.Now(), Level: "INFO", Message: "hello"}}); err != nil {
		t.Fatalf("ShipLogs: %v", err)
	}
	if err := client.NotifyHealthChanged(ctx); err != nil {
		t.Fatalf("NotifyHealthChanged: %v", err)
	}
	if got := f.sessions()["http://gpu-7:8080 gpu-7"]; len(got) != 1 || len(f.logs.batches["gpu-7#7"]) != 1 || len(f.bus.published()) != 1 {
		t.Fatalf("core saw sessions %v, logs %v, events %v", got, f.logs.batches, f.bus.published())
	}
}

// A core that answers a config read with another node's document is not
// believed.
func TestClientRefusesAnotherNodesConfig(t *testing.T) {
	f := newControlFixture(t)
	f.addNode(7, "gpu-7")
	key8 := f.addNode(8, "gpu-8")
	// The core serves node 7's document whoever asks.
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := f.server.nodeConfig(r.Context(), 7)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(core.Close)
	client, err := NewClient(core.URL, key8)
	if err != nil {
		t.Fatal(err)
	}
	if doc, _, err := client.FetchConfig(t.Context(), ""); err == nil {
		t.Fatalf("node 8 took node 7's configuration: %+v", doc)
	}
}

// The client never follows a redirect, which would send a signed request
// somewhere it was not signed for; a 401 on a call other than the config read
// asks the node to reread its configuration; failures a retry can fix are
// retryable for the log drain, others not.
func TestClientAnswers(t *testing.T) {
	var status atomic.Int32
	var followed atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		if code := int(status.Load()); code == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		} else {
			w.WriteHeader(code)
		}
	}))
	t.Cleanup(core.Close)
	key := (&fakeNodes{}).add(t, nodepool.Node{ID: 7, Type: nodepool.NodeTypeTranscode})
	client, err := NewClient(core.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	var unauthorized atomic.Int32
	client.SetOnUnauthorized(func() { unauthorized.Add(1) })
	ctx := t.Context()

	status.Store(http.StatusFound)
	if err := client.NotifyHealthChanged(ctx); Status(err) != http.StatusFound || followed.Load() {
		t.Fatalf("redirect: %v, followed %v", err, followed.Load())
	}

	status.Store(http.StatusUnauthorized)
	if _, _, err := client.FetchConfig(ctx, ""); Status(err) != http.StatusUnauthorized || unauthorized.Load() != 0 {
		t.Fatalf("config 401: %v, hook ran %d times", err, unauthorized.Load())
	}
	if err := client.NotifyHealthChanged(ctx); Status(err) != http.StatusUnauthorized || unauthorized.Load() != 1 {
		t.Fatalf("health 401: %v, hook ran %d times", err, unauthorized.Load())
	}

	entries := []opslog.Entry{{Message: "x"}}
	for code, retryable := range map[int32]bool{
		http.StatusTooManyRequests: true, http.StatusServiceUnavailable: true,
		http.StatusBadRequest: false, http.StatusRequestEntityTooLarge: false,
	} {
		status.Store(code)
		if err := client.ShipLogs(ctx, entries); err == nil || errors.Is(err, logstream.ErrRetryable) != retryable {
			t.Errorf("ShipLogs with %d: %v, retryable %v", code, err, retryable)
		}
	}
	core.Close()
	if err := client.ShipLogs(ctx, entries); !errors.Is(err, logstream.ErrRetryable) {
		t.Errorf("ShipLogs with the core down: %v, want retryable", err)
	}

	if _, err := NewClient("ftp://core", key); err == nil {
		t.Error("a non-HTTP core URL was accepted")
	}
	if _, err := NewClient(core.URL, strings.TrimPrefix(key, "snk1_")); err == nil {
		t.Error("a malformed node key was accepted")
	}
}

// ShipLogs keeps every batch within the core's entry bound.
func TestShipLogsSplitsOversizedBatches(t *testing.T) {
	f := newControlFixture(t)
	key := f.addNode(7, "gpu-7")
	core := httptest.NewServer(f.handler)
	t.Cleanup(core.Close)
	client, err := NewClient(core.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]opslog.Entry, 3*MaxLogBatchEntries)
	if err := client.ShipLogs(t.Context(), entries); err != nil {
		t.Fatalf("ShipLogs: %v", err)
	}
	if got := len(f.logs.batches["gpu-7#7"]); got != len(entries) {
		t.Fatalf("core stored %d entries, want %d", got, len(entries))
	}
}
