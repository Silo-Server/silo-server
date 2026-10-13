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
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type testNodes struct{ nodes []Node }

func (n testNodes) ByURL(_ context.Context, url string) (Node, bool, error) {
	for _, v := range n.nodes {
		if v.URL == url {
			return v, true, nil
		}
	}
	return Node{}, false, nil
}
func (n testNodes) ByID(_ context.Context, id int) (Node, bool, error) {
	for _, v := range n.nodes {
		if v.ID == id {
			return v, true, nil
		}
	}
	return Node{}, false, nil
}

type testSources struct {
	store AssignmentStore
	paths []string
}

func (s *testSources) Acquire(ctx context.Context, node int, work string, gen uint64, path, kind string) (workerwire.Source, error) {
	ok, err := s.store.IsActive(ctx, node, work, gen)
	if err != nil || !ok || path != "/private/approved.mp4" {
		return workerwire.Source{}, ErrDenied
	}
	s.paths = append(s.paths, path)
	return workerwire.Source{Ref: workerwire.SourceRef{ID: "media-ref", Kind: kind, Revision: "revision"}, Origin: "https://core.example", URL: "https://core.example/api/internal/workers/7/sources/grant", Grant: "grant", ETag: "\"revision\"", Size: 32, NodeID: node, WorkID: work, Generation: gen}, nil
}
func (s *testSources) Release(context.Context, int, string, uint64) error { return nil }
func TestTransportRegisteredWorkerHidesCorePathsAndFencesStop(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	sources := &testSources{store: store}
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	core, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", node.URL+"/transcode/start", strings.NewReader(`{"session_id":"work","input_path":"/private/approved.mp4","target_bitrate_kbps":1234}`))
	outgoing, err := core.PrepareRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if outgoing.URL.Path != "/worker/execute" {
		t.Fatalf("not dispatch envelope: %s", outgoing.URL.Path)
	}
	body, _ := io.ReadAll(outgoing.Body)
	if strings.Contains(string(body), "/private/") {
		t.Fatalf("core path escaped: %s", body)
	}
	var a workerwire.Assignment
	if err = json.Unmarshal(body, &a); err != nil {
		t.Fatal(err)
	}
	if a.WorkID != "work" || a.Generation != 1 || a.Sources["source:media-ref"].Ref.ID != "media-ref" {
		t.Fatalf("assignment=%+v", a)
	}
	if _, ok, err := store.Get(t.Context(), 7, "work"); err != nil || !ok {
		t.Fatalf("not durable before dispatch: %v %v", ok, err)
	}
	stop := httptest.NewRequest("DELETE", node.URL+"/transcode/work", nil)
	if _, err = core.PrepareRequest(stop); err != nil {
		t.Fatal(err)
	}
	if active, _ := store.IsActive(t.Context(), 7, "work", 1); active {
		t.Fatal("stop dispatched before fencing work")
	}
}
func TestTransportExactOriginAndDisabledNode(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	core, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: false}}}, Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRequest("GET", "https://worker.example/status", nil)
	if _, err = core.PrepareRequest(disabled); err == nil {
		t.Fatal("disabled node received core signature")
	}
	legacy := httptest.NewRequest("GET", "https://worker.example.attacker/status", nil)
	out, err := core.PrepareRequest(legacy)
	if err != nil || out != legacy {
		t.Fatalf("legacy origin not preserved: %v", err)
	}
	if legacy.Header.Get("X-Silo-Worker-Signature") != "" {
		t.Fatal("signature leaked to other origin")
	}
	_ = http.MethodGet
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFailedWorkerReplacementKeepsHealthyCoreGeneration(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, Sources: &testSources{store: store}})
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusAccepted
	transport := service.WrapTransport(testTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	}))
	start := func() {
		t.Helper()
		r := httptest.NewRequest("POST", node.URL+"/transcode/start", strings.NewReader(`{"session_id":"work","input_path":"/private/approved.mp4"}`))
		response, err := transport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	start()
	live, ok, err := store.Get(t.Context(), 7, "work")
	if err != nil || !ok {
		t.Fatal("initial job not activated")
	}
	status = 500
	start()
	after, ok, err := store.Get(t.Context(), 7, "work")
	if err != nil || !ok || after.Generation != live.Generation {
		t.Fatal("failed replacement killed healthy job")
	}
	if active, _ := store.IsActive(t.Context(), 7, "work", live.Generation); !active {
		t.Fatal("healthy source authority revoked")
	}
	if pending, _ := store.Pending(t.Context(), 7); len(pending) != 0 {
		t.Fatal("failed candidate remains admitted")
	}
	status = 202
	start()
	after, _, _ = store.Get(t.Context(), 7, "work")
	if after.Generation <= live.Generation {
		t.Fatal("successful replacement failed to commit")
	}
}
