package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
)

func TestOwnerUnavailableDoesNotReplaceAssignment(t *testing.T) {
	const origin = "https://core.example"
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	public, workerKey, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}
	store := NewMemoryStore()
	sourceStore := workersource.NewMemoryStore()
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	recipes := &testRecipes{card: &playback.RecipeCard{SessionID: "work", InputPath: path, TranscodeNodeURL: node.URL}}
	servers := []*httptest.Server{httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)}
	services := make([]*Service, 2)
	sources := make([]*workersource.Authority, 2)
	for i, server := range servers {
		index := i
		t.Cleanup(server.Close)
		var err error
		sources[i], err = workersource.New(workersource.Options{
			Origin: origin, OwnerURL: "http://" + server.Listener.Addr().String(), OwnerKey: coreKey, Store: sourceStore,
			PathApprover: func(context.Context, string, string) error { return nil },
			ActiveStatus: func(ctx context.Context, node int, work string, generation uint64) (bool, error) {
				return services[index].WorkActiveStatus(ctx, node, work, generation)
			},
			VerifyRequest: func(*http.Request, int) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sources[i].Close)
		services[i], err = New(Options{Origin: origin, PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, RecipeStore: recipes, Sources: statusSourceAdapter{sources[i]}, SourceHandler: sources[i].Handler()})
		if err != nil {
			t.Fatal(err)
		}
		server.Config.Handler = sources[i].WrapOwner(services[i].Handler())
		server.Start()
	}
	original, err := services[0].EnsureRecipeAssignment(t.Context(), 7, "work")
	if err != nil {
		t.Fatal(err)
	}
	_, originalSourceOK := primarySource(original)
	if !originalSourceOK {
		t.Fatal("no source")
	}
	var originalGrant string
	for _, source := range original.Sources {
		originalGrant = source.Grant
	}
	before, err := sourceStore.Get(t.Context(), originalGrant)
	if err != nil {
		t.Fatal(err)
	}
	servers[0].Close()
	// Confirm this is an actual owner transport outage, rather than expiry.
	if _, err := sources[1].Renew(t.Context(), 7, "work", original.Generation, originalGrant); !errors.Is(err, workersource.ErrUnavailable) {
		t.Fatalf("renew=%v", err)
	}
	after, err := services[1].EnsureRecipeAssignment(t.Context(), 7, "work")
	if err == nil {
		var replacementGrant string
		for _, source := range after.Sources {
			replacementGrant = source.Grant
		}
		replacement, e := sourceStore.Get(t.Context(), replacementGrant)
		if e != nil {
			t.Fatal(e)
		}
		t.Fatalf("owner outage silently migrated source: generation %d->%d owner %s->%s same identity=%v same revision=%v", original.Generation, after.Generation, before.OwnerID, replacement.OwnerID, before.Identity == replacement.Identity, before.Source.Ref.Revision == replacement.Source.Ref.Revision)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("owner outage became definitive assignment denial: %v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/api/internal/workers/7/recipes/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := workeridentity.Sign(request, origin, workeridentity.DomainNodeProof, workerKey, 7, false); err != nil {
		t.Fatal(err)
	}
	request.Host = request.URL.Host
	request.URL.Scheme = "http"
	request.URL.Host = servers[1].Listener.Addr().String()
	response, err := servers[1].Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "1" {
		t.Fatalf("owner outage recipe request must be retryable: status=%d headers=%v", response.StatusCode, response.Header)
	}
	if _, err := sourceStore.Get(t.Context(), originalGrant); err != nil {
		t.Fatalf("outage deleted original grant: %v", err)
	}
	retained, found, err := store.Get(t.Context(), 7, "work")
	if err != nil || !found || retained.Generation != original.Generation {
		t.Fatalf("outage replaced original assignment: generation=%d found=%v error=%v", retained.Generation, found, err)
	}
}
