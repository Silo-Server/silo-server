package blobstore_test

import (
	"errors"
	"io"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
)

func TestRouteFallsBackForSheetsLeftInTheOtherStore(t *testing.T) {
	assets, current, previous := blobstoretest.New(), blobstoretest.New(), blobstoretest.New()
	for store, key := range map[*blobstoretest.Memory]string{
		assets:   "tmdb/poster.jpg",
		current:  "trickplay/1/2/0.jpg",
		previous: "trickplay/1/1/0.jpg",
	} {
		if err := store.Put(t.Context(), key, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	// previous also holds a poster key; only trickplay/ keys may reach it.
	if err := previous.Put(t.Context(), "tmdb/other.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	reads := blobstore.Route(assets, "trickplay/", blobstore.Fallback(current, previous))
	for _, key := range []string{"tmdb/poster.jpg", "trickplay/1/2/0.jpg", "trickplay/1/1/0.jpg"} {
		body, _, err := reads.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
		data, _ := io.ReadAll(body)
		_ = body.Close()
		if string(data) != key {
			t.Fatalf("Get(%s) = %q", key, data)
		}
		if _, err := reads.Stat(t.Context(), key); err != nil {
			t.Fatalf("Stat(%s): %v", key, err)
		}
	}
	for _, key := range []string{"tmdb/other.jpg", "trickplay/9/9/0.jpg"} {
		if _, _, err := reads.Get(t.Context(), key); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("Get(%s) error = %v, want ErrNotFound", key, err)
		}
	}
	if err := reads.Put(t.Context(), "trickplay/1/3/0.jpg", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := current.Objects["trickplay/1/3/0.jpg"]; !ok {
		t.Fatal("write did not go to the current store")
	}
}
