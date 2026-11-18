package nodecontrol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/opslog"
)

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
