package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workersource"
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

func TestRecipeJobIdentityKeepsParentAndLegacyIdentityAcrossRecovery(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(strconv.FormatBool(legacy), func(t *testing.T) {
			public, key, _ := ed25519.GenerateKey(rand.Reader)
			store := NewMemoryStore()
			node := Node{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}
			card := &playback.RecipeCard{SessionID: "logical-parent", TranscodeTransportID: "transport-plan", InputPath: "/private/approved.mp4", TranscodeNodeURL: node.URL, OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "login"}}
			sources := &expiringSources{testSources: &testSources{store: store}}
			const secret = "job-identity-output-test"
			service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, RecipeStore: &testRecipes{card: card}, Sources: sources, OutputAuthorities: NewMemoryOutputAuthorities(), VerifyToken: func(_ context.Context, token string) (*streamtoken.Claims, error) {
				return streamtoken.Verify(token, secret)
			}})
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				body, _ := json.Marshal(card)
				a, err := service.admit(t.Context(), node, "transport-plan", transcodeStartPath, body)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Activate(t.Context(), 7, a.WorkID, a.Generation); err != nil {
					t.Fatal(err)
				}
			}
			first, err := service.EnsureRecipeAssignment(t.Context(), 7, "transport-plan")
			if err != nil {
				t.Fatal(err)
			}
			want := "logical-parent"
			if legacy {
				want = ""
			}
			if first.JobID != want {
				t.Fatalf("initial JobID=%q want=%q", first.JobID, want)
			}
			sources.expired = true
			next, err := service.EnsureRecipeAssignment(t.Context(), 7, "transport-plan")
			if err != nil || next.JobID != want || next.Generation <= first.Generation {
				t.Fatalf("recovery changed accounting identity: %+v %v", next, err)
			}
			sources.expired = false
			claims := card.ToClaims()
			token, err := streamtoken.Sign(claims, secret, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := service.IssueOutputGrant(t.Context(), token, node.URL, secret)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainProxyOutputGrant, node.URL, public, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var authority workeridentity.OutputAuthority
			if err := json.Unmarshal(grant.Body, &authority); err != nil {
				t.Fatal(err)
			}
			if authority.JobID != want {
				t.Fatalf("output grant replaced exact assignment identity: got=%q want=%q", authority.JobID, want)
			}
		})
	}
}

type expiringSources struct {
	*testSources
	expired  bool
	revision string
}

func (s *expiringSources) Renew(ctx context.Context, node int, work string, generation uint64, grant string) (workerwire.Source, error) {
	if s.expired {
		return workerwire.Source{}, workersource.ErrExpired
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
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
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
