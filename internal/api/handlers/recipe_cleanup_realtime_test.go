package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestRedisOrdinaryAndRealtimeStopsPreserveSuccessorRecipe(t *testing.T) {
	for _, path := range []string{"ordinary", "realtime-stop", "realtime-terminate"} {
		for _, changed := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "-identical", true: "-different"}[changed], func(t *testing.T) {
				client := stopRecipeRedis(t)
				store := noderecipe.NewStore(client, time.Minute)
				work := "native-realtime-stop-" + uuid.NewString()
				t.Cleanup(func() { _ = store.Delete(context.Background(), work) })
				entered, resume := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(resume) })
				defer release()
				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					if request.Method == http.MethodDelete {
						close(entered)
						<-resume
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				t.Cleanup(node.Close)
				manager := playback.NewSessionManager(0, 0)
				session, err := manager.StartSession(7, "profile", 1, playback.PlayTranscode, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: node.URL, TransportID: work}); err != nil {
					t.Fatal(err)
				}
				handler := NewPlaybackHandler(manager)
				handler.JWTSecret = "test-secret"
				handler.NodeRecipeStore = store
				card := playback.RecipeCard{SessionID: session.ID, TranscodeTransportID: work, InputPath: "/synthetic/approved", TranscodeNodeURL: node.URL, TargetBitrateKbps: 1000}
				if err := store.Put(t.Context(), work, card); err != nil {
					t.Fatal(err)
				}
				queueEntered, queueRelease := make(chan struct{}), make(chan struct{})
				releaseQueue := sync.OnceFunc(func() { close(queueRelease) })
				defer func() { releaseQueue(); waitConfirmedPauseStates(t, handler, session.ID) }()
				if path != "ordinary" {
					handler.runRealtimeResult(session.ID, true, func() { close(queueEntered); <-queueRelease })
					select {
					case <-queueEntered:
					case <-time.After(3 * time.Second):
						t.Fatal("realtime provider queue did not become busy")
					}
				}
				command := playback.CommandStop
				if path == "realtime-terminate" {
					command = playback.CommandTerminate
				}
				commandID := "stop-" + uuid.NewString()
				handler.rememberRealtimeCommand(commandID, session.ID, command)
				message := realtimeResultMessage(t, session.ID, commandID, playback.RealtimeResultStatusCompleted)
				done := make(chan error, 1)
				go func() {
					if path == "ordinary" {
						done <- handler.stopPlaybackSessionByID(t.Context(), session.ID, true)
					} else {
						done <- handler.handleRealtimeClientMessage(session.ID, message)
					}
				}()
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("stop did not reach remote cleanup", err)
				case <-time.After(3 * time.Second):
					t.Fatal("stop did not reach remote cleanup")
				}
				if changed {
					card.TargetBitrateKbps = 2000
				}
				if err := store.Put(t.Context(), work, card); err != nil {
					t.Fatal(err)
				}
				release()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("transport stop waited for queued provider finalization")
				}
				if _, err := manager.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
					t.Fatal("stop left session authority live", err)
				}
				current, found := store.Get(t.Context(), work)
				if !found || current.TargetBitrateKbps != card.TargetBitrateKbps {
					t.Fatal("old stop erased or replaced the successor publication")
				}
				releaseQueue()
				waitConfirmedPauseStates(t, handler, session.ID)
				if _, found := store.Get(t.Context(), work); !found {
					t.Fatal("queued finalization erased the successor publication")
				}
			})
		}
	}
}

func stopRecipeRedis(t *testing.T) *redis.Client {
	t.Helper()
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
	return client
}

// Ordinary stop may wait for a running start-side effect before reaching
// transport cleanup. Capture must precede that wait as well as the node DELETE.
func TestRedisOrdinaryStopCapturesRecipeBeforeStartEffectCancellation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "different"}[changed], func(t *testing.T) {
			store := noderecipe.NewStore(stopRecipeRedis(t), time.Minute)
			work := "native-start-stop-" + uuid.NewString()
			t.Cleanup(func() { _ = store.Delete(context.Background(), work) })
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			t.Cleanup(node.Close)
			card := playback.RecipeCard{SessionID: work, InputPath: "/synthetic/approved", TranscodeNodeURL: node.URL, TargetBitrateKbps: 1000}
			if err := store.Put(t.Context(), work, card); err != nil {
				t.Fatal(err)
			}
			canceled, effectsDone := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(effectsDone) })
			defer release()
			handler := &PlaybackHandler{tm: playback.NewTranscodeManager(), NodeRecipeStore: store}
			handler.tm.JWTSecretFn = func() string { return "test-secret" }
			handler.v3StartEffectsPending = map[string]*playbackStartSideEffectsStateV3{work: {ctx: context.Background(), cancel: func() { close(canceled) }, done: effectsDone, started: true}}
			done := make(chan struct{})
			go func() {
				handler.finalizeSessionStop(t.Context(), &playback.Session{ID: work, TranscodeTransportID: work, TranscodeNodeURL: node.URL}, false, "test", false)
				close(done)
			}()
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("ordinary stop did not enter start-effect cancellation")
			}
			if changed {
				card.TargetBitrateKbps = 2000
			}
			if err := store.Put(t.Context(), work, card); err != nil {
				t.Fatal(err)
			}
			release()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("ordinary stop did not finish")
			}
			current, found := store.Get(t.Context(), work)
			if !found || current.TargetBitrateKbps != card.TargetBitrateKbps {
				t.Fatal("ordinary cleanup erased the successor published during start-effect wait")
			}
		})
	}
}
