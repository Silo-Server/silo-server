package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-secret" {
			t.Error("wrong command authority or method")
		}
		entered <- r.URL.Path
		<-release
		if r.URL.Path == "/first/admin/force-reload" {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	repo := &reloadNodeRepo{stubNodeRepository: stubNodeRepository{nodes: []*nodepool.Node{
		{ID: 1, Name: "First", URL: server.URL + "/first", Enabled: true},
		{ID: 9, Name: "Disabled", URL: server.URL + "/disabled", Enabled: false},
		{ID: 2, Name: "Second", URL: server.URL + "/second", Enabled: true},
	}}}
	h := NewNodeHandler(repo, nil, nil, nil, nil, nil, "synthetic-secret")
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
				if r.Method != "POST" || r.URL.Path != "/admin/force-reload" || r.Header.Get("Authorization") != "Bearer synthetic-secret" {
					t.Error("wrong command")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer server.Close()
			repo := &stubNodeRepository{node: &nodepool.Node{ID: 17, URL: server.URL, Enabled: false}}
			h := NewNodeHandler(repo, nil, nil, nil, nil, nil, "synthetic-secret")
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
	h := NewNodeHandler(repo, nil, nil, nil, nil, nil, "")
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
