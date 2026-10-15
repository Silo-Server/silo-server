package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type reloadNodeRepo struct {
	stubNodeRepository
	err error
}

func (r *reloadNodeRepo) List(context.Context) ([]*nodepool.Node, error) { return r.nodes, r.err }
func TestAdminNodeReloadBulk(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	node := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/admin/force-reload" || !nodeauthtest.Signed(r, "synthetic-secret") {
			t.Error("wrong command authority or method")
		}
		entered <- r.RequestURI
		<-release
		if r.RequestURI == "/first/admin/force-reload" {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(503)
		}
	})
	// Each node sits under its own path prefix, which a reverse proxy strips:
	// workers serve their routes from the root.
	gateway := http.NewServeMux()
	for _, prefix := range []string{"/first", "/second", "/disabled"} {
		gateway.Handle(prefix+"/", http.StripPrefix(prefix, node))
	}
	server := httptest.NewServer(gateway)
	defer server.Close()
	repo := &reloadNodeRepo{stubNodeRepository: stubNodeRepository{nodes: []*nodepool.Node{
		{ID: 1, Name: "First", URL: server.URL + "/first", Enabled: true},
		{ID: 9, Name: "Disabled", URL: server.URL + "/disabled", Enabled: false},
		{ID: 2, Name: "Second", URL: server.URL + "/second", Enabled: true},
	}}}
	h := NewNodeHandler(repo, nil, nil, nil, nil, nil, nodeauthtest.SharedSecret("synthetic-secret"))
	done := make(chan []ForceReloadResult, 1)
	go func() {
		rows, err := h.ForceReloadAdminNodes(t.Context())
		if err != nil {
			t.Error(err)
		}
		done <- rows
	}()
	<-entered
	<-entered
	close(release)
	rows := <-done
	if calls.Load() != 2 || len(rows) != 2 || rows[0].NodeID != 1 || rows[0].Status != "ok" || rows[1].NodeID != 2 || rows[1].Status != "error" {
		t.Fatal(rows, calls.Load())
	}
	repo.nodes = nil
	rows, err := h.ForceReloadAdminNodes(t.Context())
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	repo.err = errors.New("store unavailable")
	if _, err = h.ForceReloadAdminNodes(t.Context()); !errors.Is(err, repo.err) {
		t.Fatal(err)
	}
	if _, err = (*NodeHandler)(nil).ForceReloadAdminNodes(t.Context()); !errors.Is(err, ErrAdminNodesUnavailable) {
		t.Fatal(err)
	}
}
func TestAdminNodeReloadSingle(t *testing.T) {
	for _, status := range []int{200, 204, 307, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls, redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(204) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/admin/force-reload" || !nodeauthtest.Signed(r, "synthetic-secret") {
					t.Error("wrong command")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer server.Close()
			repo := &stubNodeRepository{node: &nodepool.Node{ID: 17, URL: server.URL, Enabled: false}}
			h := NewNodeHandler(repo, nil, nil, nil, nil, nil, nodeauthtest.SharedSecret("synthetic-secret"))
			rows, err := h.ForceReloadAdminNode(t.Context(), 17)
			if err != nil || len(rows) != 1 || rows[0].NodeID != 17 || calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatal(rows, err, calls.Load(), redirected.Load())
			}
			if (rows[0].Status == "ok") != (status == 200 || status == 204) {
				t.Fatal(rows)
			}
		})
	}
}
func TestAdminNodeReloadMissingAndCanceled(t *testing.T) {
	repo := &stubNodeRepository{}
	h := NewNodeHandler(repo, nil, nil, nil, nil, nil, nodeauthtest.SharedSecret(""))
	if _, err := h.ForceReloadAdminNode(t.Context(), 1); !errors.Is(err, nodepool.ErrNodeNotFound) {
		t.Fatal(err)
	}
	if _, err := (*NodeHandler)(nil).ForceReloadAdminNode(t.Context(), 1); !errors.Is(err, ErrAdminNodesUnavailable) {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	repo.node = &nodepool.Node{ID: 1, URL: server.URL}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := h.ForceReloadAdminNode(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].Status != "error" || calls.Load() != 0 {
		t.Fatal(rows, err, calls.Load())
	}
	repo.node.URL = ":invalid-url"
	rows, err = h.ForceReloadAdminNode(t.Context(), 1)
	if err != nil || rows[0].Status != "error" || rows[0].Error != "" {
		t.Fatal(rows, err)
	}
}

// reloadEvents records recipe revocations and the node calls they precede.
type reloadEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *reloadEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *reloadEvents) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	events := e.events
	e.events = nil
	return events
}

type recordingRecipeAuthority struct {
	events    *reloadEvents
	revokeErr error
}

func (a recordingRecipeAuthority) RevokeNodeID(_ context.Context, nodeID int) error {
	a.events.add("revoke id " + strconv.Itoa(nodeID))
	return a.revokeErr
}

func (a recordingRecipeAuthority) RevokeNode(_ context.Context, nodeURL string) error {
	a.events.add("revoke url " + nodeURL)
	return nil
}

// A transcode node no longer holds the recipe store, so the core withdraws
// every recipe the node could rebuild a session from, by row id and URL,
// before it tells the node to tear its sessions down. A failed revocation
// leaves the node alone for the operator's retry. Proxies hold no recipes.
func TestAdminNodeForceReloadRevokesTranscodeRecipesFirst(t *testing.T) {
	events := &reloadEvents{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events.add("node " + r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	repo := &stubNodeRepository{node: &nodepool.Node{ID: 17, Type: nodepool.NodeTypeTranscode, URL: server.URL, Enabled: true}}
	h := NewNodeHandler(repo, nil, nil, nil, nil, nil, nodeauthtest.SharedSecret("synthetic-secret"))
	h.SetRecipeAuthority(recordingRecipeAuthority{events: events})

	rows, err := h.ForceReloadAdminNode(t.Context(), 17)
	if err != nil || len(rows) != 1 || rows[0].Status != "ok" {
		t.Fatal(rows, err)
	}
	want := []string{"revoke id 17", "revoke url " + server.URL, "node /admin/force-reload"}
	if got := events.take(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	h.SetRecipeAuthority(recordingRecipeAuthority{events: events, revokeErr: errors.New("redis unavailable")})
	rows, err = h.ForceReloadAdminNode(t.Context(), 17)
	if err != nil || len(rows) != 1 || rows[0].Status == "ok" {
		t.Fatal(rows, err)
	}
	if got := events.take(); slices.Contains(got, "node /admin/force-reload") {
		t.Fatalf("events = %v: the node was reloaded with its recipes still valid", got)
	}

	repo.node = &nodepool.Node{ID: 18, Type: nodepool.NodeTypeProxy, URL: server.URL, Enabled: true}
	if rows, err = h.ForceReloadAdminNode(t.Context(), 18); err != nil || rows[0].Status != "ok" {
		t.Fatal(rows, err)
	}
	if got := events.take(); !slices.Equal(got, []string{"node /admin/force-reload"}) {
		t.Fatalf("proxy reload events = %v", got)
	}
}
