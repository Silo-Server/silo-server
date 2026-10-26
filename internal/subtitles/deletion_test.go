package subtitles

import (
	"context"
	"errors"
	"testing"
)

type deletionObjectSpy struct {
	BlobStore
	keys []string
	err  error
}

func (s *deletionObjectSpy) Delete(_ context.Context, key string) error {
	s.keys = append(s.keys, key)
	return s.err
}

func TestSubtitleGuardedDeleteRequiresRepositorySupport(t *testing.T) {
	objects := &deletionObjectSpy{}
	err := NewManager(newMockSubtitleRepo(), objects).DeleteSubtitleWithRevision(t.Context(), 1, new(int64(1)))
	if !errors.Is(err, ErrSubtitleGuardedDeletionUnavailable) || len(objects.keys) != 0 {
		t.Fatalf("unsupported: %v %v", err, objects.keys)
	}
}
