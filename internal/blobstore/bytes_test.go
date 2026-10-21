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
