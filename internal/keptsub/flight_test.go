package keptsub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
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

func TestSharedBoundsDetachedFlights(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			// Only the node's limit applies here.
			s := &Store{flights: make(map[flightKey]*flight), fileFlightLimit: maxWholeFlights}
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			var admitted []*flight
			defer func() {
				unblock()
				for _, f := range admitted {
					<-f.done
				}
			}()
			const limit = 16
			for i := range 2 * limit {
				ctx, cancel := context.WithCancel(t.Context())
				entered := make(chan *flight, 1)
				done := make(chan error, 1)
				key := flightKey{fileID: 1, stream: i}
				if background {
					// A canceled prefetch leaves the join map before its
					// worker stops; retries must still count toward the cap.
					key = flightKey{fileID: 1}
				}
				go func() {
					_, err := s.shared(ctx, key, background, 0, func(readCtx context.Context, f *flight) (*Track, error) {
						// The read has its slot: a request's goes on without it.
						_ = s.start(readCtx, f)
						entered <- f
						<-release
						return nil, nil
					})
					done <- err
				}()
				select {
				case f := <-entered:
					admitted = append(admitted, f)
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled waiter: %v", err)
					}
				case err := <-done:
					cancel()
					if err == nil || errors.Is(err, context.Canceled) {
						t.Fatalf("admission refusal: %v", err)
					}
				}
			}
			if len(admitted) != limit {
				t.Errorf("admitted %d detached workers, want %d", len(admitted), limit)
			}
			if !background {
				ctx, leave := context.WithCancel(t.Context())
				defer leave()
				joined := make(chan error, 1)
				go func() {
					_, err := s.shared(ctx, flightKey{fileID: 1}, false, 0,
						func(context.Context, *flight) (*Track, error) {
							return nil, errors.New("started another read instead of joining")
						})
					joined <- err
				}()
				waitFor(t, "a caller to join at capacity", func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					return admitted[0].waiters == 1
				})
				leave()
				if err := <-joined; !errors.Is(err, context.Canceled) {
					t.Errorf("join at capacity: %v", err)
				}
			}
			unblock()
			for _, f := range admitted {
				<-f.done
			}
			if _, err := s.shared(t.Context(), flightKey{fileID: 100}, false, 0,
				func(context.Context, *flight) (*Track, error) { return nil, nil }); err != nil {
				t.Fatalf("capacity after workers finish: %v", err)
			}
		})
	}
}

// stallStartedRead makes a read wait, once it holds a read slot, until
// release closes or its context ends.
func stallStartedRead(s *Store, release <-chan struct{}) *atomic.Int32 {
	var calls atomic.Int32
	probe := s.probeStart
	s.probeStart = func(ctx context.Context, path string) (time.Duration, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		return probe(ctx, path)
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

// A request's read that has started finishes and keeps the track after the
// request is gone; a prefetch's stops, and a later caller starts afresh.
func TestReadsOutliveRequestsButNotPrefetches(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	release := make(chan struct{})
	calls := stallStartedRead(s, release)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)

	ctx, leave := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := s.WholeTrack(ctx, path, ref)
		done <- err
	}()
	waitFor(t, "the read", func() bool { return calls.Load() == 1 })
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
	local, release, err := s.mirror(t.Context(), kept)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
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

// Releasing a copy's pin scans the mirror directory only while pinned copies
// have left it over budget, so a warm read doesn't stat every copy.
func TestPinReleaseScansOnlyOverBudget(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(newMemRepo(), &memBlobs{}, Options{MirrorDir: func() string { return dir }, MirrorMaxBytes: 1 << 20})
	write := func(body string) (string, func()) {
		t.Helper()
		path, _, _, release, err := s.writeMirror(dir, ".mks", func(w io.Writer) error {
			_, err := io.WriteString(w, body)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return path, release
	}
	// A scan removes a temp file a crash left; its presence shows none ran.
	staleTemp := func() string {
		t.Helper()
		path := filepath.Join(dir, mirrorTempPrefix+"stale")
		old := time.Now().Add(-2 * staleMirrorTempAge)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first, release := write("first")
	release()
	stale := staleTemp()
	s.pinMirror(first)()
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("a release under budget scanned the directory: %v", err)
	}
	// Over budget with the new copy pinned, its release scans.
	s.opts.MirrorMaxBytes = 1
	_, release = write("second")
	stale = staleTemp()
	release()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the release that left the directory over budget didn't scan: %v", err)
	}
}

// One source file's tracks take at most fileFlightLimit of a node's whole
// reads: a request for another track of that file is answered busy at once,
// while other files' tracks are still admitted.
func TestWholeReadsPerFileAreBounded(t *testing.T) {
	s, _, _ := newTestStore(t, "ffmpeg")
	s.fileFlightLimit = 2
	release := make(chan struct{})
	read := func(ctx context.Context, _ *flight) (*Track, error) {
		select {
		case <-release:
			return &Track{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	results := make(chan error, 3)
	for _, key := range []flightKey{{fileID: 1, stream: 1}, {fileID: 1, stream: 2}, {fileID: 2, stream: 1}} {
		go func() {
			_, err := s.shared(t.Context(), key, false, 0, read)
			results <- err
		}()
	}
	waitFor(t, "the reads to be admitted", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.activeFlights == 3
	})
	// A busy answer comes before any wait; an admitted read would stop
	// waiting after a millisecond instead.
	if _, err := s.shared(t.Context(), flightKey{fileID: 1, stream: 3}, false, time.Millisecond, read); !errors.Is(err, playback.ErrSubtitleReadsBusy) {
		t.Fatalf("a third read of one file: %v", err)
	}
	close(release)
	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// A read still waiting for a slot stops when its last caller goes, whether
// the request ended or stopped waiting, and keeps nothing.
func TestQueuedReadStopsWhenItsCallersGo(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	for _, tc := range []struct {
		name string
		wait time.Duration
		want error
	}{
		{"request ended", time.Hour, context.Canceled},
		{"request stopped waiting", 20 * time.Millisecond, errStillReading},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _ := newTestStore(t, ffmpeg)
			s.foregroundWait = tc.wait
			// Every read slot is taken.
			for range wholeReads {
				s.reads <- struct{}{}
			}
			ctx, leave := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := s.WholeTrack(ctx, path, *playback.EmbeddedSubtitleRefFor(file, 0))
				done <- err
			}()
			waitFor(t, "the read to queue", func() bool {
				_, claims := repo.counts()
				return claims == 1
			})
			if errors.Is(tc.want, context.Canceled) {
				leave()
			}
			if err := <-done; !errors.Is(err, tc.want) {
				t.Fatalf("request: %v, want %v", err, tc.want)
			}
			waitFor(t, "the queued read to stop", func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.activeFlights == 0 && len(s.flights) == 0
			})
			leave()
			for range wholeReads {
				<-s.reads
			}
			if puts, _ := repo.counts(); puts != 0 {
				t.Fatal("a stopped read kept the track")
			}
		})
	}
}
