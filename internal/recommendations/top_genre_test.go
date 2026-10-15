package recommendations

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The top genre is read once and served to every viewer until it expires; a
// failed read is not kept.
func TestTopGenreIsReadOncePerTTL(t *testing.T) {
	var cache topGenreCache
	var calls atomic.Int32
	failing := true
	load := func(context.Context) (string, error) {
		calls.Add(1)
		if failing {
			return "", errors.New("db down")
		}
		return "Drama", nil
	}
	if _, err := cache.get(t.Context(), load); err == nil {
		t.Fatal("want the failed read's error")
	}
	failing = false
	for range 3 {
		if genre, err := cache.get(t.Context(), load); err != nil || genre != "Drama" {
			t.Fatalf("genre = %q, %v; want Drama", genre, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("reads = %d, want 2: the failed one and one for every later viewer", calls.Load())
	}
}

// An expired genre is served at once while a background read refreshes it.
func TestTopGenreServesExpiredGenreWhileRefreshing(t *testing.T) {
	cache := topGenreCache{genre: "Drama", loaded: true, expires: time.Now().Add(-time.Second)}
	release := make(chan struct{})
	loaded := make(chan struct{})
	load := func(context.Context) (string, error) {
		<-release
		defer close(loaded)
		return "Comedy", nil
	}
	if genre, err := cache.get(t.Context(), load); err != nil || genre != "Drama" {
		t.Fatalf("genre = %q, %v; want the expired Drama without waiting", genre, err)
	}
	close(release)
	<-loaded
	deadline := time.Now().Add(5 * time.Second)
	for {
		if genre, _ := cache.get(t.Context(), load); genre == "Comedy" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background read did not store Comedy")
		}
		time.Sleep(time.Millisecond)
	}
}

// A read waiting on the first read stops when its request ends.
func TestTopGenreStopsWaitingWhenTheReadEnds(t *testing.T) {
	var cache topGenreCache
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := cache.get(ctx, func(context.Context) (string, error) {
		<-release
		return "Drama", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the request's cancellation", err)
	}
}
