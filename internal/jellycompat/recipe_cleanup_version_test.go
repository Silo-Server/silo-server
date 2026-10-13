package jellycompat

import (
	"context"
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

func TestRedisCompatCleanupPreservesIdenticalRecipePublishedDuringStop(t *testing.T) {
	testRedisCompatRecipeCleanup(t, false)
}

func TestRedisCompatRemoteToLocalPreservesRecipePublishedDuringStop(t *testing.T) {
	testRedisCompatRecipeCleanup(t, true)
}

func testRedisCompatRecipeCleanup(t *testing.T, remoteToLocal bool) {
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
	store := noderecipe.NewStore(client, time.Minute)
	work := "compat-cleanup-" + uuid.NewString()
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
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/media", TranscodeNodeURL: node.URL}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	h := &PlaybackHandler{tm: playback.NewTranscodeManager(), RecipeNodeStore: store}
	h.tm.JWTSecretFn = func() string { return "test-secret" }
	if remoteToLocal {
		state := NewPlaybackSessionStore(time.Hour, nil)
		state.Put(PlaybackSession{ID: "compat-play", UpstreamSessionID: work, Recipe: &card})
		h.playbackStore = state
		h.sessionMgr = &testCompatSessionManager{sessions: map[string]*playback.Session{work: {ID: work, UserID: 7}}}
	}
	done := make(chan error, 1)
	go func() {
		if remoteToLocal {
			done <- h.persistTranscodeRecipe(t.Context(), "compat-play", work, playback.TranscodeOpts{SessionID: work, InputPath: card.InputPath})
			return
		}
		h.cleanupPlaySession(t.Context(), &PlaybackSession{ID: "compat-play", UpstreamSessionID: work}, nil, node.URL)
		done <- nil
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not reach remote stop")
	}
	// Identical bytes still represent a separately published replacement.
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("recipe cleanup failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not finish")
	}
	if _, ok := store.Get(t.Context(), work); !ok {
		t.Fatal("old compat cleanup erased identical successor recipe")
	}
}
