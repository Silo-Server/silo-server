package blobstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
)

func TestMutationFenceBlockedWriteHonorsContext(t *testing.T) {
	base, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		store := WithMutationFence(base)
		release, err := store.(MutationFencer).BeginMutationFence(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- store.Put(ctx, "tmdb/poster.webp", []byte("image")) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("write passed an active fence: %v", err)
		default:
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked write error = %v, want context.Canceled", err)
		}
	})
}

func TestPauseMutationsFencesSharedStoreOnceAndResumes(t *testing.T) {
	base, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		store := WithMutationFence(base)
		// A local root is both the assets and the operational store.
		resume, err := PauseMutations(t.Context(), store, store, nil)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- store.Put(context.Background(), "tmdb/poster.webp", []byte("image")) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("write passed paused mutations: %v", err)
		default:
		}
		resume()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestPauseMutationsReleasesTakenFencesWhenCanceled(t *testing.T) {
	first, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		assets, operational := WithMutationFence(first), WithMutationFence(second)
		held, err := operational.(MutationFencer).BeginMutationFence(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer held()
		ctx, cancel := context.WithCancel(t.Context())
		paused := make(chan error, 1)
		go func() {
			_, err := PauseMutations(ctx, assets, operational)
			paused <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-paused; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled pause = %v", err)
		}
		// The assets fence taken before the cancel must be released.
		if err := assets.Put(t.Context(), "tmdb/poster.webp", []byte("image")); err != nil {
			t.Fatal(err)
		}
	})
}

// partSizedStore records the part size a multipart write asked for.
type partSizedStore struct {
	*Filesystem
	partSize int64
}

func (s *partSizedStore) PutStreamParts(ctx context.Context, key string, r io.Reader, contentType string, partSize int64) error {
	s.partSize = partSize
	return s.PutStream(ctx, key, r, contentType)
}

// A fenced store keeps a backup's part size: the operational store is the
// fenced assets store when no private bucket is configured.
func TestMutationFenceForwardsPartSize(t *testing.T) {
	base, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner := &partSizedStore{Filesystem: base}
	sized, ok := WithMutationFence(inner).(interface {
		PutStreamParts(ctx context.Context, key string, r io.Reader, contentType string, partSize int64) error
	})
	if !ok {
		t.Fatal("a fenced store does not take a part size")
	}
	if err := sized.PutStreamParts(t.Context(), "backups/x", strings.NewReader("dump"), "", 64<<20); err != nil || inner.partSize != 64<<20 {
		t.Fatalf("part size reached the store as %d (%v), want %d", inner.partSize, err, 64<<20)
	}
}
