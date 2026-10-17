package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type pausedRecipeDelete struct {
	*noderecipe.Store
	entered, resume chan struct{}
	once            sync.Once
}

func (s *pausedRecipeDelete) pause() {
	s.once.Do(func() { close(s.entered); <-s.resume })
}
func (s *pausedRecipeDelete) Delete(ctx context.Context, work string) error {
	s.pause()
	return s.Store.Delete(ctx, work)
}
func (s *pausedRecipeDelete) RecipeVersion(ctx context.Context, work string) (*playback.RecipeCard, string, error) {
	versioned, ok := any(s.Store).(interface {
		RecipeVersion(context.Context, string) (*playback.RecipeCard, string, error)
	})
	if !ok {
		return nil, "", errors.New("recipe version API unavailable")
	}
	return versioned.RecipeVersion(ctx, work)
}
func (s *pausedRecipeDelete) DeleteVersion(ctx context.Context, work, version string) error {
	s.pause()
	versioned, ok := any(s.Store).(interface {
		DeleteVersion(context.Context, string, string) error
	})
	if !ok {
		return errors.New("recipe version API unavailable")
	}
	return versioned.DeleteVersion(ctx, work, version)
}

type recipeStopSources struct{ store AssignmentStore }

func (s recipeStopSources) Acquire(ctx context.Context, node int, work string, generation uint64, _, kind string) (workerwire.Source, error) {
	active, err := s.store.IsActive(ctx, node, work, generation)
	if err != nil || !active {
		return workerwire.Source{}, ErrDenied
	}
	return workerwire.Source{Ref: workerwire.SourceRef{ID: "media-ref", Kind: kind, Revision: "revision"}, Origin: "https://core.example", URL: fmt.Sprintf("https://core.example/api/internal/workers/%d/sources/grant", node), Grant: "grant", ETag: `"revision"`, Size: 32, NodeID: node, WorkID: work, Generation: generation}, nil
}
func (recipeStopSources) Release(context.Context, int, string, uint64) error { return nil }

type unavailableRecipePublication struct {
	*testRecipes
	version string
	err     error
}

func (s unavailableRecipePublication) RecipeVersion(context.Context, string) (*playback.RecipeCard, string, error) {
	return s.card, s.version, s.err
}

func (unavailableRecipePublication) DeleteVersion(context.Context, string, string) error {
	return errors.New("unavailable publication must never authorize deletion")
}

func TestStopRequiresSafeRecipePublicationBeforeFencing(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}
	for _, scenario := range []string{"unsupported", "missing-version", "capture-error", "missing-card", "nil-store"} {
		t.Run(scenario, func(t *testing.T) {
			base := NewMemoryStore()
			a, err := base.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: "work"})
			if err != nil {
				t.Fatal(err)
			}
			if err := base.Complete(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if err := base.Activate(t.Context(), node.ID, a.WorkID, a.Generation); err != nil {
				t.Fatal(err)
			}
			card := &playback.RecipeCard{SessionID: "work", TranscodeNodeURL: node.URL, RoutingExecutionNodeID: node.ID}
			var recipes RecipeStore
			switch scenario {
			case "unsupported":
				recipes = &testRecipes{card: card}
			case "missing-version":
				recipes = unavailableRecipePublication{testRecipes: &testRecipes{card: card}}
			case "capture-error":
				recipes = unavailableRecipePublication{testRecipes: &testRecipes{card: card}, err: ErrUnavailable}
			case "missing-card":
				recipes = unavailableRecipePublication{testRecipes: &testRecipes{}}
			}
			service, err := New(Options{Origin: "https://core.example", PrivateKey: private, Nodes: testNodes{[]Node{node}}, Store: base, RecipeStore: recipes})
			if err != nil {
				t.Fatal(err)
			}
			err = service.stopAt(t.Context(), node, "work", a.Generation)
			_, live, lookupErr := base.Get(t.Context(), node.ID, "work")
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if scenario == "nil-store" || scenario == "missing-card" {
				if err != nil || live {
					t.Fatal("absent recipe prevented safe cleanup", err)
				}
			} else if !errors.Is(err, ErrUnavailable) || !live {
				t.Fatal("unsafe recipe capture fenced work or fell back to deletion", err)
			}
		})
	}
}

type pausedGenerationFence struct {
	*RedisStore
	entered, resume chan struct{}
}

func (s *pausedGenerationFence) StopIfGeneration(ctx context.Context, node int, work string, generation uint64) error {
	if err := s.RedisStore.StopIfGeneration(ctx, node, work, generation); err != nil {
		return err
	}
	close(s.entered)
	<-s.resume
	return nil
}

func TestRedisLateStopKeepsSuccessorAdmissionRenewing(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires task Redis SILO_TEST_REDIS_URL")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	prefix := "silo:recipe-admission-test:" + uuid.NewString() + ":"
	base := NewRedisStore(client, prefix)
	t.Cleanup(func() {
		keys, err := client.Keys(context.Background(), prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	store := &pausedGenerationFence{RedisStore: base, entered: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(store.resume) })
	t.Cleanup(release)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: private, Nodes: testNodes{[]Node{node}}, Store: store, Sources: recipeStopSources{base}})
	if err != nil {
		t.Fatal(err)
	}
	start := func() *http.Request {
		t.Helper()
		r, err := service.PrepareRequest(httptest.NewRequest(http.MethodPost, node.URL+transcodeStartPath, strings.NewReader(`{"session_id":"work","input_path":"/private/approved.mp4"}`)))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	old := start()
	response := &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody}
	if err := service.FinishRequest(old, response, nil); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	stopped := make(chan error, 1)
	go func() {
		_, err := service.PrepareRequest(httptest.NewRequest(http.MethodDelete, node.URL+"/transcode/work", nil))
		stopped <- err
	}()
	<-store.entered
	next := start()
	t.Cleanup(func() { _ = service.FinishRequest(next, nil, errors.New("test dispatch completed")) })
	candidate, ok, err := base.Candidate(t.Context(), node.ID, "work")
	if err != nil || !ok {
		t.Fatal("successor candidate missing", err)
	}
	release()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	// The shared lease must renew beyond its original two-second deadline,
	// including when another core asks whether this candidate is owned.
	timer := time.NewTimer(admissionLeaseTTL + 300*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if leased, err := base.HasLease(t.Context(), node.ID, "work", candidate.Generation); err != nil || !leased {
		t.Fatal("old stop canceled successor lease renewal", err)
	}
	service.mu.Lock()
	admission := service.admissions[workKey{node.ID, "work"}]
	service.mu.Unlock()
	if admission.generation != candidate.Generation || admission.ctx.Err() != nil {
		t.Fatal("old stop removed successor local admission")
	}
}

//nolint:funlen // The delayed stop and successor publication must race on the same Redis recipe and ownership fence.
func TestRedisLateCoreStopPreservesSuccessorRecipePublication(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires task Redis SILO_TEST_REDIS_URL")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"different", "identical", "other-node"} {
		t.Run(scenario, func(t *testing.T) {
			work := "recipe-stop-" + uuid.NewString()
			prefix := "silo:recipe-stop-test:" + uuid.NewString() + ":"
			store := NewRedisStore(client, prefix)
			recipes := noderecipe.NewStore(client, time.Minute)
			t.Cleanup(func() {
				_ = recipes.Delete(context.Background(), work)
				keys, err := client.Keys(context.Background(), prefix+"*").Result()
				if err == nil && len(keys) > 0 {
					_ = client.Del(context.Background(), keys...).Err()
				}
			})
			paused := &pausedRecipeDelete{Store: recipes, entered: make(chan struct{}), resume: make(chan struct{})}
			release := sync.OnceFunc(func() { close(paused.resume) })
			t.Cleanup(release)
			public, private, _ := ed25519.GenerateKey(rand.Reader)
			nodes := testNodes{[]Node{{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}, {ID: 8, URL: "https://second-worker.example", PublicKey: public, Enabled: true}}}
			newService := func(recipeStore RecipeStore) *Service {
				s, err := New(Options{Origin: "https://core.example", PrivateKey: private, Nodes: nodes, Store: store, RecipeStore: recipeStore, Sources: recipeStopSources{store}, ExecutionActive: func(ctx context.Context, _ Node, _ workerwire.Assignment) bool {
					_, ok := recipes.Get(ctx, work)
					return ok
				}})
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			one, two := newService(paused), newService(recipes)
			start := func(service *Service, node Node, bitrate int) uint64 {
				t.Helper()
				body := fmt.Sprintf(`{"session_id":%q,"input_path":"/private/approved.mp4","target_bitrate_kbps":%d}`, work, bitrate)
				request, err := service.PrepareRequest(httptest.NewRequest(http.MethodPost, node.URL+transcodeStartPath, strings.NewReader(body)))
				if err != nil {
					t.Fatal(err)
				}
				response := &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody}
				if err := service.FinishRequest(request, response, nil); err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				// This is the same unchanged native/Jelly store publication used
				// after an accepted remote start, including identical replacements.
				if err := recipes.Put(t.Context(), work, playback.RecipeCard{SessionID: work, InputPath: "/private/approved.mp4", TranscodeNodeURL: node.URL, RoutingExecutionNodeID: node.ID, TargetBitrateKbps: bitrate}); err != nil {
					t.Fatal(err)
				}
				a, ok, err := store.Get(t.Context(), node.ID, work)
				if err != nil || !ok {
					t.Fatal("start did not publish", err)
				}
				return a.Generation
			}
			old := start(one, nodes.nodes[0], 1000)
			stopped := make(chan error, 1)
			go func() {
				_, err := one.PrepareRequest(httptest.NewRequest(http.MethodDelete, nodes.nodes[0].URL+"/transcode/"+work, nil))
				stopped <- err
			}()
			select {
			case <-paused.entered:
			case err := <-stopped:
				t.Fatal("stop did not reach recipe cleanup", err)
			case <-time.After(3 * time.Second):
				t.Fatal("stop did not reach recipe cleanup")
			}
			bitrate, nextNode := 2000, nodes.nodes[0]
			if scenario == "identical" {
				bitrate = 1000
			}
			if scenario == "other-node" {
				nextNode = nodes.nodes[1]
			}
			next := start(two, nextNode, bitrate)
			if scenario != "other-node" && next <= old {
				t.Fatal("successor generation did not advance")
			}
			if _, err := two.currentRecipe(httptest.NewRequest(http.MethodGet, "/", nil), nextNode, work); err != nil {
				t.Fatal("successor not reconstructible before delayed cleanup", err)
			}
			release()
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if a, ok, err := store.Get(t.Context(), nextNode.ID, work); err != nil || !ok || a.Generation != next {
				t.Fatal("successor assignment removed", err)
			}
			if _, err := two.currentRecipe(httptest.NewRequest(http.MethodGet, "/", nil), nextNode, work); err != nil {
				t.Fatalf("old stop erased %s successor's durable recipe: %v", scenario, err)
			}
		})
	}
}
