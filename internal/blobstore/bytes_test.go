package blobstore

import (
	"context"
	"testing"
)

// shortDeleteStore reports a nil error and a zero count, which is what S3 batch
// deletion does when the request succeeds but the object-level delete inside it
// fails. Discarding the count would read that as success.
type shortDeleteStore struct {
	Store
	count int
}

func (s *shortDeleteStore) Delete(context.Context, []string) (int, error) { return s.count, nil }

func TestByteStoreDeleteReportsAShortCountAsFailure(t *testing.T) {
	store := NewByteStore(&shortDeleteStore{count: 0})
	if err := store.Delete(context.Background(), "subtitles/1/a.srt"); err == nil {
		t.Fatal("a failed object delete was reported as success")
	}
}

func TestByteStoreRoundTripsThroughTheFilesystem(t *testing.T) {
	fs, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := NewByteStore(fs)
	ctx := context.Background()
	const key = "subtitles/42/a.srt"

	if err := store.Put(ctx, key, []byte("cue")); err != nil {
		t.Fatal(err)
	}
	data, err := store.Get(ctx, key)
	if err != nil || string(data) != "cue" {
		t.Fatalf("read back %q: %v", data, err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, key); err == nil {
		t.Fatal("object survived deletion")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("repeated deletion of an absent key: %v", err)
	}
	if NewByteStore(nil) != nil {
		t.Fatal("nil store produced a non-nil ByteStore")
	}
}
