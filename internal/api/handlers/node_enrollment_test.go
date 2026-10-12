package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type enrollmentConfigurationStore struct {
	scriptedNodeConfiguration
	input nodepool.CreateNodeInput
	calls int
	err   error
}

func (s *enrollmentConfigurationStore) Create(_ context.Context, in nodepool.CreateNodeInput) (*nodepool.Node, error) {
	s.calls++
	s.input = in
	if s.err != nil {
		return nil, s.err
	}
	return &nodepool.Node{ID: 17, AdminRevision: 1, Name: in.Name, Type: in.Type, URL: in.URL, MaxJobs: in.MaxJobs}, nil
}
func TestEnrollAdminNodeClampsCapacityAndRecoversRegistration(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		ceiling, requested, want int
	}{{"unlimited", 0, 9, 9}, {"provider", 4, 9, 4}, {"lower", 4, 2, 2}, {"empty", 4, 0, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != nodeenrollment.ReceiptPath || r.Header.Get("Authorization") != "Bearer test-jwt" {
					t.Error("unexpected enrollment retry")
					w.WriteHeader(401)
					return
				}
				_ = json.NewEncoder(w).Encode(nodeenrollment.Receipt{BindingID: nodeenrollment.Identity("test-master-key"), EncodingCapacity: encodinglimit.Capacity{MaxJobs: tc.ceiling}})
			}))
			defer srv.Close()
			store := &enrollmentConfigurationStore{err: errors.New("temporary registry failure")}
			h := &NodeHandler{}
			h.SetConfigurationStore(store)
			h.SetEnrollmentClient(&nodeenrollment.Client{Bootstrap: nodeenrollment.Binding{SecretKey: "test-master-key"}, JWTSecret: "test-jwt"})
			requested := tc.requested
			in := nodepool.CreateNodeInput{Name: "Sold node", Type: "transcode", URL: srv.URL, MaxJobs: &requested}
			if _, err := h.EnrollAdminNode(t.Context(), in, nodeenrollment.Input{Token: "already retired"}); err == nil {
				t.Fatal("registry failure lost")
			}
			store.err = nil
			node, err := h.EnrollAdminNode(t.Context(), in, nodeenrollment.Input{Token: "already retired"})
			if err != nil {
				t.Fatal(err)
			}
			if node.MaxJobs == nil || *node.MaxJobs != tc.want || store.calls != 2 || requests != 2 {
				t.Fatalf("node=%+v calls=%d requests=%d", node, store.calls, requests)
			}
			if requested != tc.requested {
				t.Fatal("clamp mutated caller")
			}
		})
	}
}
func TestEnrollAdminNodeRejectsBeforeHandoff(t *testing.T) {
	h := &NodeHandler{}
	store := &enrollmentConfigurationStore{}
	h.SetConfigurationStore(store)
	h.SetEnrollmentClient(&nodeenrollment.Client{})
	for _, in := range []nodepool.CreateNodeInput{{Name: "Proxy", Type: "proxy", URL: "http://node.invalid"}, {Type: "transcode", URL: "http://node.invalid"}} {
		if _, err := h.EnrollAdminNode(t.Context(), in, nodeenrollment.Input{}); err == nil {
			t.Fatal("invalid enrollment accepted")
		}
	}
	if store.calls != 0 {
		t.Fatal("invalid input persisted")
	}
}
