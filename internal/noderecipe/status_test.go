package noderecipe

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestRecipeStatusDistinguishesMissingRevokedAndUnavailable(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires task Redis SILO_TEST_REDIS_URL")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()
	store := NewStore(client, time.Minute)
	work := "recipe-status-" + uuid.NewString()
	node := "https://" + work + ".example.test"
	defer func() {
		cleanup := redis.NewClient(options)
		defer func() { _ = cleanup.Close() }()
		_, _ = cleanup.Del(context.Background(), store.key(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work), publicationVersionKey(work), nodeAuthorityGenerationKey(node)).Result()
	}()
	if card, found, err := store.GetStatus(t.Context(), work); err != nil || found || card != nil {
		t.Fatal("missing recipe did not return definitive absence")
	}
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/media", TranscodeNodeURL: node, PlayMethod: playback.PlayRemux}
	if err = store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetStatus(t.Context(), work); err != nil || !found {
		t.Fatal("live recipe was not returned")
	}
	if err = client.Set(t.Context(), store.key(work), "{invalid-json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.GetStatus(t.Context(), work); err == nil {
		t.Fatal("malformed recipe became a confirmed missing recipe")
	}
	if err = store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	if err = store.RevokeNode(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetStatus(t.Context(), work); err != nil || found {
		t.Fatalf("node epoch revoke was not a definite denial: found=%v err=%v", found, err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.GetStatus(t.Context(), work); err == nil {
		t.Fatal("unavailable Redis became a confirmed missing recipe")
	}
}
