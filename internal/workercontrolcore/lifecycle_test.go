package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type countingNodes struct {
	testNodes
	lookups atomic.Int64
}

func (n *countingNodes) ByID(ctx context.Context, id int) (Node, bool, error) {
	n.lookups.Add(1)
	return n.testNodes.ByID(ctx, id)
}
func TestSourceAuthorityCacheAvoidsPerChunkLookupAndStopInvalidates(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	nodes := &countingNodes{testNodes: testNodes{[]Node{node}}}
	service, _ := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: nodes, Store: store})
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, "work", a.Generation); err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		if !service.WorkActive(t.Context(), 7, "work", a.Generation) {
			t.Fatal("live work denied")
		}
	}
	if nodes.lookups.Load() != 1 {
		t.Fatalf("lookup per source block: %d", nodes.lookups.Load())
	}
	if err = service.stop(t.Context(), node, "work"); err != nil {
		t.Fatal(err)
	}
	if service.WorkActive(t.Context(), 7, "work", a.Generation) {
		t.Fatal("stopped source cache remained allowed")
	}
}
func TestStreamedBackgroundKeepsAuthorityUntilBodyEnds(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	service, _ := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, Sources: &testSources{store: store}})
	var work string
	transport := service.WrapTransport(testTransport(func(r *http.Request) (*http.Response, error) {
		var a workerwire.Assignment
		_ = json.NewDecoder(r.Body).Decode(&a)
		work = a.WorkID
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("complete")), Request: r}, nil
	}))
	response, err := transport.RoundTrip(httptest.NewRequest("POST", node.URL+"/trickplay/extract", strings.NewReader(`{"input_path":"/private/approved.mp4"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !service.HasInFlight(7, work) {
		t.Fatal("background context ended at response headers")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "complete" {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if active, _ := store.Active(t.Context(), 7); len(active) != 0 {
		t.Fatal("completed synchronous background source was retained")
	}
}
func TestSecondCoreAcceptsOnlyShortOwnedPendingLease(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	options := Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, Sources: &testSources{store: store}}
	one, _ := New(options)
	two, _ := New(options)
	out, err := one.PrepareRequest(httptest.NewRequest("POST", node.URL+"/transcode/start", strings.NewReader(`{"session_id":"work","input_path":"/private/approved.mp4"}`)))
	if err != nil {
		t.Fatal(err)
	}
	candidate, ok, err := store.Candidate(t.Context(), 7, "work")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if !two.WorkActive(t.Context(), 7, "work", candidate.Generation) {
		t.Fatal("other core cannot serve active candidate")
	}
	if err = one.FinishRequest(out, &http.Response{StatusCode: 500, Body: http.NoBody}, nil); err != nil {
		t.Fatal(err)
	}
	// The second core's cache is bounded; after its250ms horizon the durable
	// candidate record/lease must not authorize anything.
	time.Sleep(260 * time.Millisecond)
	if two.WorkActive(t.Context(), 7, "work", candidate.Generation) {
		t.Fatal("abandoned candidate remained authorized")
	}
}

func TestPlaybackPublicationRequiresAcceptedStart(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	service, _ := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, Sources: &testSources{store: store}})
	out, err := service.PrepareRequest(httptest.NewRequest("POST", node.URL+transcodeStartPath, strings.NewReader(`{"session_id":"work","input_path":"/private/approved.mp4"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.FinishRequest(out, &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil); err == nil {
		t.Fatal("non-accepted playback response published")
	}
	if active, _ := store.Active(t.Context(), node.ID); len(active) != 0 {
		t.Fatal("unaccepted playback remained live")
	}
}
