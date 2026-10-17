package workercontrolcore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestOtherCoreRenewsAndRestoresThroughExactSourceOwner(t *testing.T) {
	const origin = "https://core.example"
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	workerPublic, workerKey, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: workerPublic, Enabled: true}
	store := NewMemoryStore()
	sourceStore := workersource.NewMemoryStore()
	servers := []*httptest.Server{httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)}
	services := make([]*Service, 2)
	sources := make([]*workersource.Authority, 2)
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, server := range servers {
		t.Cleanup(server.Close)
		index := i
		var err error
		sources[i], err = workersource.New(workersource.Options{Origin: origin, OwnerURL: "http://" + server.Listener.Addr().String(), OwnerKey: coreKey, Store: sourceStore, PathApprover: func(context.Context, string, string) error { return nil }, ActiveStatus: func(ctx context.Context, node int, work string, generation uint64) (bool, error) {
			return services[index].WorkActiveStatus(ctx, node, work, generation)
		}, VerifyRequest: func(r *http.Request, node int) error {
			return services[index].VerifyRequest(r, node, workeridentity.DomainSourceRequest)
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sources[i].Close)
		services[i], err = New(Options{Origin: origin, PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, Sources: statusSourceAdapter{sources[i]}, SourceHandler: sources[i].Handler()})
		if err != nil {
			t.Fatal(err)
		}
		server.Config.Handler = sources[i].WrapOwner(services[i].Handler())
		server.Start()
	}
	assignment := activateAuthorityWork(t, store, "live")
	descriptor, err := sources[0].Acquire(t.Context(), node.ID, assignment.WorkID, assignment.Generation, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	assignment.Sources = map[string]workerwire.Source{"input": descriptor}
	before, err := sourceStore.Get(t.Context(), descriptor.Grant)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"work_id": assignment.WorkID, "generation": assignment.Generation})
	r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, descriptor.URL+"/renew", bytes.NewReader(body))
	r.Header.Set("X-Silo-Source-Grant", descriptor.Grant)
	if err = workeridentity.Sign(r, origin, workeridentity.DomainNodeProof, workerKey, node.ID, false); err != nil {
		t.Fatal(err)
	}
	r.Host = r.URL.Host
	r.URL.Scheme = "http"
	r.URL.Host = servers[1].Listener.Addr().String()
	response, err := servers[1].Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("cross-core worker renewal HTTP%d %s", response.StatusCode, data)
	}
	var reply GrantReply
	if err = json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSourceGrant, node.URL, coreKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var renewed workerwire.Source
	if json.Unmarshal(grant.Body, &renewed) != nil || renewed != descriptor {
		t.Fatal("renewal changed the source descriptor")
	}
	restored, err := services[1].restoreSources(t.Context(), node, assignment)
	if err != nil || restored.Generation != assignment.Generation || restored.Sources["input"] != descriptor {
		t.Fatalf("cross-owner restore changed authority: %+v %v", restored, err)
	}
	after, err := sourceStore.Get(t.Context(), descriptor.Grant)
	if err != nil || !after.ExpiresAt.After(before.ExpiresAt) {
		t.Fatalf("renewal did not reach real owner: %v", err)
	}
}
