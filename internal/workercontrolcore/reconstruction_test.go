package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerwire"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestThemeRecipeReconstructionUsesCatalogThemeSource(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	recipes := &testRecipes{card: &playback.RecipeCard{SessionID: "theme-work", InputPath: "/private/approved.mp4", TranscodeNodeURL: node.URL, PlayMethod: streamtoken.PlayMethodThemeAAC, AudioOnly: true}}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, RecipeStore: recipes, Sources: &testSources{store: store}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := service.EnsureRecipeAssignment(t.Context(), node.ID, "theme-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Sources) != 1 {
		t.Fatalf("unexpected sources: %+v", a.Sources)
	}
	for _, source := range a.Sources {
		if source.Ref.Kind != sourceTheme {
			t.Fatalf("theme admitted as %q", source.Ref.Kind)
		}
	}
}

func TestStoppedAssignmentCannotReconstructFromSurvivingRecipe(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	recipes := &testRecipes{card: &playback.RecipeCard{SessionID: "work", InputPath: "/private/approved.mp4", TranscodeNodeURL: node.URL}}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, RecipeStore: recipes, Sources: &testSources{store: store}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.EnsureRecipeAssignment(t.Context(), 7, "work"); err != nil {
		t.Fatal(err)
	}
	if err = store.Stop(t.Context(), 7, "work"); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after the durable fence but before deleting the card.
	if _, err = service.EnsureRecipeAssignment(t.Context(), 7, "work"); err == nil {
		t.Fatal("stopped work resurrected from surviving card")
	}
}

type expiringSources struct {
	*testSources
	expired  bool
	revision string
}

func (s *expiringSources) Renew(ctx context.Context, node int, work string, generation uint64, grant string) (workerwire.Source, error) {
	if s.expired {
		return workerwire.Source{}, ErrUnavailable
	}
	return s.Acquire(ctx, node, work, generation, "/private/approved.mp4", sourceMedia)
}
func (s *expiringSources) Acquire(ctx context.Context, node int, work string, generation uint64, path, kind string) (workerwire.Source, error) {
	source, err := s.testSources.Acquire(ctx, node, work, generation, path, kind)
	if s.revision != "" {
		source.Ref.Revision = s.revision
	}
	return source, err
}
func TestExpiredSourceRecoveryRequiresOriginalRevision(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			store := NewMemoryStore()
			node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
			recipes := &testRecipes{card: &playback.RecipeCard{SessionID: "work", InputPath: "/private/approved.mp4", TranscodeNodeURL: node.URL}}
			sources := &expiringSources{testSources: &testSources{store: store}}
			service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, RecipeStore: recipes, Sources: sources})
			if err != nil {
				t.Fatal(err)
			}
			old, err := service.EnsureRecipeAssignment(t.Context(), 7, "work")
			if err != nil {
				t.Fatal(err)
			}
			sources.expired = true
			if changed {
				sources.revision = "changed"
			}
			next, err := service.EnsureRecipeAssignment(t.Context(), 7, "work")
			if changed {
				if err == nil {
					t.Fatal("expired grant silently switched bytes")
				}
				live, ok, e := store.Get(t.Context(), 7, "work")
				if e != nil || !ok || live.Generation != old.Generation {
					t.Fatal("bad recovery replaced healthy authority")
				}
			} else if err != nil || next.Generation <= old.Generation || next.Sources["source:media-ref"].Ref.Revision != "revision" {
				t.Fatalf("same-source recovery failed: %+v %v", next, err)
			}
		})
	}
}
