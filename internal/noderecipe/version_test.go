package noderecipe

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

//nolint:gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestRedisRecipePublicationVersionsProtectIdenticalWritesAndLegacyMigration(t *testing.T) {
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
	store := NewStore(client, time.Minute)
	work := "recipe-version-" + uuid.NewString()
	t.Cleanup(func() { _ = store.Delete(context.Background(), work) })
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/media", TranscodeNodeURL: "https://worker.example"}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	_, old, err := store.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := client.Get(t.Context(), store.key(work)).Bytes()
	if err := store.PutTTL(t.Context(), work, card, time.Second); err != nil {
		t.Fatal(err)
	}
	_, current, err := store.RecipeVersion(t.Context(), work)
	if err != nil || current == old || !validPublicationVersion(current) {
		t.Fatalf("identical publication did not rotate: err=%v", err)
	}
	after, _ := client.Get(t.Context(), store.key(work)).Bytes()
	if !bytes.Equal(before, after) {
		t.Fatal("publication version changed legacy recipe JSON")
	}
	if ttl, err := client.PTTL(t.Context(), publicationVersionKey(work)).Result(); err != nil || ttl <= 0 || ttl > time.Second {
		t.Fatal("publication sidecar did not inherit bounded lifetime", err)
	}
	for _, token := range []string{old, ""} {
		if err := store.DeleteVersion(t.Context(), work, token); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.Get(t.Context(), work); !ok {
			t.Fatal("stale or blank token deleted current publication")
		}
	}
	// Simulate a pre-upgrade record with no publication token. Capture adopts
	// that record atomically; a current writer still replaces its token.
	if err := client.Del(t.Context(), publicationVersionKey(work)).Err(); err != nil {
		t.Fatal(err)
	}
	_, migrated, err := store.RecipeVersion(t.Context(), work)
	if err != nil || !validPublicationVersion(migrated) {
		t.Fatal("legacy publication was not adopted", err)
	}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteVersion(t.Context(), work, migrated); err != nil {
		t.Fatal(err)
	}
	_, latest, err := store.RecipeVersion(t.Context(), work)
	if err != nil || latest == migrated {
		t.Fatal("legacy capture erased later identical publication", err)
	}
	if err := store.DeleteVersion(t.Context(), work, latest); err != nil {
		t.Fatal(err)
	}
	if card, version, err := store.RecipeVersion(t.Context(), work); err != nil || card != nil || version != "" {
		t.Fatal("current deletion retained publication", err)
	}
	if err := store.DeleteVersion(t.Context(), work, latest); err != nil {
		t.Fatal("missing publication deletion is not idempotent", err)
	}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), publicationVersionKey(work), "corrupt", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecipeVersion(t.Context(), work); err == nil {
		t.Fatal("corrupt publication version was accepted")
	}
	if err := store.DeleteVersion(t.Context(), work, "corrupt"); err == nil {
		t.Fatal("corrupt publication token authorized deletion")
	}
	if _, ok := store.Get(t.Context(), work); !ok {
		t.Fatal("invalid version capture/deletion erased recipe")
	}
}

func TestDisabledRecipePublicationVersionIsSafe(t *testing.T) {
	for _, store := range []*Store{nil, NewStore(nil, 0)} {
		card, version, err := store.RecipeVersion(t.Context(), "work")
		if err != nil || card != nil || version != "" {
			t.Fatal("disabled publication capture did not miss safely", err)
		}
		if err := store.DeleteVersion(t.Context(), "work", ""); err != nil {
			t.Fatal(err)
		}
	}
}
