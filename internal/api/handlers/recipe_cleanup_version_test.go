package handlers

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

func TestRedisNativeCleanupPreservesIdenticalRecipePublishedDuringStop(t *testing.T) {
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
	work := "native-cleanup-" + uuid.NewString()
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
	h := &PlaybackHandler{tm: playback.NewTranscodeManager(), NodeRecipeStore: store}
	h.tm.JWTSecretFn = func() string { return "test-secret" }
	done := make(chan struct{})
	go func() {
		h.finalizeSessionStop(t.Context(), &playback.Session{ID: work, TranscodeTransportID: work, TranscodeNodeURL: node.URL}, false, "test", false)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not reach remote stop")
	}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish")
	}
	if _, ok := store.Get(t.Context(), work); !ok {
		t.Fatal("old native cleanup erased identical successor recipe")
	}
}
