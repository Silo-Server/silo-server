package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestRestoreSourcesPropagatesUnknownExecutionAuthority(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			node := Node{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}
			store := NewMemoryStore()
			options := Options{Origin: "https://core.example", PrivateKey: private, Nodes: testNodes{[]Node{node}}, Store: store, Sources: &testSources{store: store}}
			if batch {
				options.ExecutionStatuses = func(context.Context, Node, []workerwire.Assignment) ([]bool, error) { return nil, ErrUnavailable }
			} else {
				options.ExecutionStatus = func(context.Context, Node, workerwire.Assignment) (bool, error) { return false, ErrUnavailable }
			}
			service, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			assignment := activateAuthorityWork(t, store, "work")
			if _, err := service.restoreSources(t.Context(), node, assignment); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unknown execution authority became success or definitive denial: %v", err)
			}
			retained, found, err := store.Get(t.Context(), node.ID, assignment.WorkID)
			if err != nil || !found || retained.Generation != assignment.Generation {
				t.Fatalf("unknown authority changed durable work: found=%v generation=%d error=%v", found, retained.Generation, err)
			}
		})
	}
}
