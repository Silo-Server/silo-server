package keptsub

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// stallSecondLookup makes the read's own lookup, the second Track call,
// wait until release closes or its context ends.
func stallSecondLookup(repo *memRepo, release <-chan struct{}) *atomic.Int32 {
	var calls atomic.Int32
	repo.trackHook = func(ctx context.Context) error {
		if calls.Add(1) != 2 {
			return nil
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return &calls
}

// A request for a track a prefetch on the same node is reading joins that
// read and raises its priority, instead of waiting on the claim.
func TestRequestJoinsAPrefetchRead(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	release := make(chan struct{})
	calls := stallSecondLookup(repo, release)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	prefetched := make(chan error, 1)
	go func() { prefetched <- s.Prefetch(t.Context(), path, ref) }()
	waitFor(t, "the prefetch read", func() bool { return calls.Load() == 2 })
	requested := make(chan error, 1)
	go func() {
		_, err := s.WholeTrack(t.Context(), path, ref)
		requested <- err
	}()
	var f *flight
	waitFor(t, "the request to join", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, fl := range s.flights {
			f = fl
		}
		return f != nil && f.waiters == 2
	})
	if !s.isForeground(f) || f.prio == nil {
		t.Fatal("the request didn't promote the prefetch read")
	}
	select {
	case <-f.prio.Done():
	default:
		t.Fatal("the read's priority wasn't raised")
	}
	close(release)
	if err := <-prefetched; err != nil {
		t.Fatal(err)
	}
	if err := <-requested; err != nil {
		t.Fatal(err)
	}
	if puts, claims := repo.counts(); puts != 1 || claims != 1 {
		t.Fatalf("%d reads and %d keeps", claims, puts)
	}
}

// A request's read finishes and keeps the track after the request is gone;
// a prefetch's stops, and a later caller starts afresh.
func TestReadsOutliveRequestsButNotPrefetches(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	release := make(chan struct{})
	calls := stallSecondLookup(repo, release)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)

	ctx, leave := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := s.WholeTrack(ctx, path, ref)
		done <- err
	}()
	waitFor(t, "the read", func() bool { return calls.Load() == 2 })
	leave()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("request: %v", err)
	}
	close(release)
	waitFor(t, "the track to be kept", func() bool {
		tr, _ := repo.Track(t.Context(), file.ID, 1)
		return tr != nil
	})

	release = make(chan struct{})
	calls = stallSecondLookup(repo, release)
	other := *playback.EmbeddedSubtitleRefFor(file, 1)
	ctx, leave = context.WithCancel(t.Context())
	go func() { done <- s.Prefetch(ctx, path, other) }()
	waitFor(t, "the prefetch read", func() bool { return calls.Load() == 2 })
	leave()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("prefetch: %v", err)
	}
	s.mu.Lock()
	left := len(s.flights)
	s.mu.Unlock()
	if left != 0 {
		t.Fatal("a stopped prefetch read can still be joined")
	}
	close(release)
}

func TestKeepQueuesTheObjectBeforeItsRow(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, blobs := newTestStore(t, ffmpeg)
	repo.putErr = errors.New("database unavailable")
	if _, err := s.WholeTrack(t.Context(), path, *playback.EmbeddedSubtitleRefFor(file, 0)); err == nil {
		t.Fatal("a track whose row failed was served")
	}
	if len(repo.queued) != 1 || blobs.objects[repo.queued[0]] == nil {
		t.Fatalf("queued %v for %d objects", repo.queued, len(blobs.objects))
	}
}

// Using a local copy leaves its file as written, so the index cache, keyed
// by modification time, keeps finding it.
func TestLocalCopiesAreNotTouched(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
		t.Fatal(err)
	}
	kept, _ := repo.Track(t.Context(), file.ID, 1)
	local, err := s.mirror(t.Context(), kept)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(local)
	time.Sleep(10 * time.Millisecond) //nolint:forbidigo // lets a touch show in the modification time
	for range 3 {
		if _, err := s.Window(t.Context(), path, ref, 0, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := os.Stat(local); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("a local copy's modification time changed")
	}
}

func TestNoLocalDirectoryKeepsNothing(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	repo, blobs := newMemRepo(), &memBlobs{}
	s := NewStore(repo, blobs, Options{FFmpegPath: func() string { return ffmpeg }})
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	if _, err := s.WholeTrack(t.Context(), path, ref); !errors.Is(err, errNoMirrorDir) {
		t.Fatalf("whole track: %v", err)
	}
	if _, err := s.DemuxCopy(t.Context(), path, ref); !errors.Is(err, errNoMirrorDir) {
		t.Fatalf("demux copy: %v", err)
	}
	if _, claims := repo.counts(); claims != 0 || len(blobs.objects) != 0 {
		t.Fatal("read or stored a track it can't serve")
	}
}
