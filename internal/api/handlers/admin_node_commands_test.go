package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type commandNodeRepo struct {
	stubNodeRepository
	persistedURL string
	persistErr   error
}

func (r *commandNodeRepo) UpdateHealth(_ context.Context, _ int, url string, _ bool, _, _ int, _ []byte, _ netaccess.NodeNetworkAccess) error {
	r.persistedURL = url
	return r.persistErr
}
func TestAdminNodeCheckView(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" || r.Method != "GET" {
			t.Error("wrong worker request", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"active_jobs":2,"egress_kbps":8,"capabilities_hash":"observed"}`))
	}))
	defer server.Close()
	repo := &commandNodeRepo{stubNodeRepository: stubNodeRepository{node: &nodepool.Node{ID: 1, URL: server.URL, Type: nodepool.NodeTypeTranscode}}}
	h := NewNodeHandler(repo, nil, nil, nil, nil, nil, "")
	out, err := h.CheckAdminNode(t.Context(), 1)
	if err != nil || !out.Healthy || out.ActiveJobs != 2 || out.EgressKbps != 8 || !out.HealthPersisted || repo.persistedURL != server.URL {
		t.Fatal(out, err, repo.persistedURL)
	}
	repo.persistErr = errors.New("store unavailable")
	out, err = h.CheckAdminNode(t.Context(), 1)
	if err != nil || !out.Healthy || out.HealthPersisted {
		t.Fatal(out, err)
	}
	repo.node = nil
	if _, err = h.CheckAdminNode(t.Context(), 1); !errors.Is(err, nodepool.ErrNodeNotFound) {
		t.Fatal(err)
	}
	if _, err = (*NodeHandler)(nil).CheckAdminNode(t.Context(), 1); !errors.Is(err, ErrAdminNodesUnavailable) {
		t.Fatal(err)
	}
}
func TestAdminNodeReprobeView(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		refreshErr error
	}{{"success", 200, nil}, {"store failure", 200, errors.New("store unavailable")}, {"busy", 409, nil}, {"unavailable", 503, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			url, auth := fakeNodeServer(t, tc.status, `{"resolved":"qsv","capability_hash":"new"}`)
			repo := &stubNodeRepository{node: &nodepool.Node{ID: 1, Name: "Synthetic", URL: url, Type: nodepool.NodeTypeTranscode}}
			refresh := &stubCapabilityRefresher{err: tc.refreshErr}
			h := NewNodeHandler(repo, nil, nil, nil, nil, nil, "synthetic-secret")
			h.SetCapabilityRefresher(refresh)
			writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			before := time.Now()
			out, err := h.ReprobeAdminNode(writer, httptest.NewRequest("POST", "/", nil).WithContext(t.Context()), 1)
			if err != nil || *auth != "Bearer synthetic-secret" || !writer.deadline.After(before) {
				t.Fatal(out, err, *auth, writer.deadline)
			}
			if tc.status == 200 {
				if out.Status != "ok" || out.CapabilityHash != "new" || out.CapabilitiesRefreshed != (tc.refreshErr == nil) || refresh.calls.Load() != 1 {
					t.Fatal(out)
				}
			} else if out.Status != "error" || refresh.calls.Load() != 0 {
				t.Fatal(out)
			}
		})
	}
}
