package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type standaloneRecipeNodes struct{ node workercontrolcore.Node }

func (n standaloneRecipeNodes) ByURL(_ context.Context, origin string) (workercontrolcore.Node, bool, error) {
	return n.node, workeridentity.SameOrigin(n.node.URL, origin, n.node.AllowHTTP), nil
}

func TestStandaloneNativeRecipeFailureRollsBackBeforePublishingRoute(t *testing.T) {
	for _, headerAuth := range []bool{false, true} {
		for _, scenario := range []string{"missing", "disabled", "write-error"} {
			t.Run(scenario+map[bool]string{false: "-legacy", true: "-header"}[headerAuth], func(t *testing.T) {
				var starts, stops atomic.Int32
				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					if request.Method == http.MethodDelete {
						stops.Add(1)
						w.WriteHeader(http.StatusNoContent)
						return
					}
					starts.Add(1)
					var start transcodenode.TranscodeStartRequest
					if err := json.NewDecoder(request.Body).Decode(&start); err != nil {
						t.Error(err)
					}
					writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: start.SessionID, Status: "started", AudioRecipeVersion: start.AudioRecipeVersion})
				}))
				t.Cleanup(node.Close)
				nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper { return base }, func(context.Context, string) (bool, error) { return true, nil })
				t.Cleanup(func() { nodehttp.Configure(nil) })
				handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
				handler.JWTSecret = "core-only-test-secret"
				grants := &recordingRecipeCardStoreV3{}
				handler.ProxyGrantStore = grants
				switch scenario {
				case "disabled":
					handler.NodeRecipeStore = &recordingRecipeCardStoreV3{disabled: true}
				case "write-error":
					handler.NodeRecipeStore = &recordingRecipeCardStoreV3{putErr: errors.New("recipe store unavailable")}
				}
				mode := mediaAuthModeV3{}
				if headerAuth {
					mode = authorizedOriginsModeV3()
				}
				plan := nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}, ProxyNode: &nodepool.Node{URL: "https://proxy.example"}}
				transport, transportErr := handler.prepareRemoteTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), &playback.Session{ID: "standalone-required", UserID: 7}, v3HandlerFixtureFile(t), remoteHLSResultV3(), plan, preparedTimelineV3{}, mode)
				if transportErr == nil || !transportErr.retryable || transport.transportID != "" {
					t.Fatal("standalone route was admitted without durable authority", transportErr)
				}
				if starts.Load() != 1 || stops.Load() != 1 || len(grants.cards) != 0 {
					t.Fatal("failed recipe admission did not retire accepted work before proxy publication")
				}
			})
		}
	}
}

func TestStandaloneNativeClassificationFailureDoesNotStartLegacyWork(t *testing.T) {
	var calls atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(node.Close)
	nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper { return base }, func(ctx context.Context, _ string) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	})
	t.Cleanup(func() { nodehttp.Configure(nil) })
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "core-only-test-secret"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	transport, transportErr := handler.prepareRemoteTransportV3(request, &playback.Session{ID: "standalone-classifier"}, v3HandlerFixtureFile(t), remoteHLSResultV3(), nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}}, preparedTimelineV3{}, mediaAuthModeV3{})
	if transportErr == nil || !transportErr.retryable || transport.transportID != "" || calls.Load() != 0 {
		t.Fatal("classification failure fell back to legacy dispatch", transportErr)
	}
}

func (n standaloneRecipeNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, n.node.ID == id, nil
}

type standaloneRecipeSources struct{}

func (standaloneRecipeSources) Acquire(_ context.Context, node int, work string, generation uint64, _, kind string) (workerwire.Source, error) {
	return workerwire.Source{Ref: workerwire.SourceRef{ID: "media-ref", Kind: kind, Revision: "current-revision"}, Origin: "https://core.example", URL: "https://core.example/api/internal/workers/17/sources/grant", Grant: "grant", ETag: `"revision"`, Size: 100, NodeID: node, WorkID: work, Generation: generation}, nil
}

func (standaloneRecipeSources) Release(context.Context, int, string, uint64) error { return nil }

func TestRedisStandaloneNativeLegacyClientKeepsDurableRecipe(t *testing.T) {
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
	for _, headerAuth := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-client", true: "header-client"}[headerAuth], func(t *testing.T) {
			var startedWork string
			started := make(chan string, 1)
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != "/worker/execute" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var assignment workerwire.Assignment
				if err := json.NewDecoder(request.Body).Decode(&assignment); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var start transcodenode.TranscodeStartRequest
				if err := json.Unmarshal(assignment.Body, &start); err != nil {
					t.Error(err)
					return
				}
				if request.Header.Get(workeridentity.HeaderDomain) != workeridentity.DomainCoreCommand || start.InputPath != "source:media-ref" {
					t.Error("fixture did not receive a registered standalone assignment")
				}
				started <- assignment.WorkID
				writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: start.SessionID, Status: "started", AudioRecipeVersion: start.AudioRecipeVersion})
			}))
			t.Cleanup(node.Close)
			public, private, _ := ed25519.GenerateKey(rand.Reader)
			registered := workercontrolcore.Node{ID: 17, URL: node.URL, PublicKey: public, Enabled: true, AllowHTTP: true}
			prefix := "silo:standalone-recipe-test:" + uuid.NewString() + ":"
			assignments := workercontrolcore.NewRedisStore(client, prefix)
			recipes := noderecipe.NewStore(client, time.Minute)
			authorityOptions := workercontrolcore.Options{Origin: "https://core.example", PrivateKey: private, Nodes: standaloneRecipeNodes{registered}, Store: assignments, RecipeStore: recipes, Sources: standaloneRecipeSources{}, ExecutionActive: func(ctx context.Context, _ workercontrolcore.Node, assignment workerwire.Assignment) bool {
				_, found := recipes.Get(ctx, assignment.WorkID)
				return found
			}}
			service, err := workercontrolcore.New(authorityOptions)
			if err != nil {
				t.Fatal(err)
			}
			nodehttp.Configure(service.WrapTransport, service.RequiresRecipe)
			t.Cleanup(func() {
				nodehttp.Configure(nil)
				_ = recipes.Delete(context.Background(), startedWork)
				keys, err := client.Keys(context.Background(), prefix+"*").Result()
				if err == nil && len(keys) > 0 {
					_ = client.Del(context.Background(), keys...).Err()
				}
			})
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "core-only-test-secret"
			handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{ID: registered.ID, URL: node.URL}}}
			handler.NodeRecipeStore = recipes
			mode := mediaAuthModeV3{}
			if headerAuth {
				mode = headerAuthenticatedMediaV3([]string{playback.FeatureHeaderAuthenticatedMediaV3})
			}
			file := v3HandlerFixtureFile(t)
			transport, transportErr := handler.prepareRemoteTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), &playback.Session{ID: "standalone-legacy-" + uuid.NewString(), UserID: 7, ProfileID: "profile"}, file, remoteHLSResultV3(), nodepool.Plan{TranscodeNode: &nodepool.Node{ID: registered.ID, URL: node.URL}}, preparedTimelineV3{}, mode)
			if transportErr != nil {
				t.Fatalf("prepare standalone transport: %#v", transportErr)
			}
			defer transport.rollback()
			startedWork = <-started
			card, found := recipes.Get(t.Context(), startedWork)
			if !found || card == nil || card.TranscodeTransportID != startedWork || card.TranscodeNodeURL != node.URL || card.InputPath != file.FilePath {
				t.Fatal("native client auth mode left standalone work without its durable core recipe")
			}
			a, found, err := assignments.Get(t.Context(), registered.ID, startedWork)
			if err != nil || !found || !service.WorkActive(t.Context(), registered.ID, startedWork, a.Generation) {
				t.Fatal("published recipe did not sustain current worker authority", err)
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for service.HasInFlight(registered.ID, startedWork) {
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("start admission never yielded to durable recipe authority")
				}
			}
			// A fresh core has no in-process admission or authority cache. Its
			// signed recipe handler must still reconstruct after the shared
			// grace expired, without any customer token from the worker.
			fresh, err := workercontrolcore.New(authorityOptions)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "https://core.example/api/internal/workers/17/recipes/"+startedWork, nil)
			if err := workeridentity.Sign(request, authorityOptions.Origin, workeridentity.DomainNodeProof, private, registered.ID, false); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			fresh.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK || !fresh.WorkActive(t.Context(), registered.ID, startedWork, a.Generation) {
				t.Fatal("fresh core denied durable standalone reconstruction after grace", response.Code)
			}
			var reply workercontrolcore.GrantReply
			if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			grant, err := workeridentity.DecodeGrant(reply.Grant, "silo/worker-recipe/v1", registered.URL, public, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var recipe workercontrolcore.RecipeReply
			if err := json.Unmarshal(grant.Body, &recipe); err != nil || recipe.Recipe.InputPath != "source:media-ref" || grant.WorkID != startedWork {
				t.Fatal("worker reconstruction did not carry a scoped opaque source", err)
			}
			transport.rollback()
			if _, found := recipes.Get(t.Context(), startedWork); found {
				t.Fatal("rollback retained standalone recipe authority")
			}
		})
	}
}
