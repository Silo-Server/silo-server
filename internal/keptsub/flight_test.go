package keptsub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type blockedMirrorBlobs struct {
	Blobs
	entered chan struct{}
	release <-chan struct{}
}

func (b *blockedMirrorBlobs) Get(context.Context, string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	return io.NopCloser(&blockedMirrorReader{blobs: b}), blobstore.ObjectInfo{}, nil
}

type blockedMirrorReader struct {
	blobs   *blockedMirrorBlobs
	started bool
}

func (r *blockedMirrorReader) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		p[0] = 'x'
		return 1, nil
	}
	r.blobs.entered <- struct{}{}
	<-r.blobs.release
	return 0, io.ErrUnexpectedEOF
}

func TestMirrorBoundsDetachedFetches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int64
		limit int
	}{{"bytes", 128 << 20, 2}, {"workers", 1024, 8}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				blobs := &blockedMirrorBlobs{entered: make(chan struct{}, 1), release: release}
				var stores []*Store
				for range 2 {
					dir := t.TempDir()
					stores = append(stores, NewStore(newMemRepo(), blobs, Options{MirrorDir: func() string { return dir }}))
				}
				var admitted []*mirrorFetch
				defer func() {
					unblock()
					for _, f := range admitted {
						<-f.done
					}
				}()
				first := &Track{SHA256: fmt.Sprintf("%064x", 1), Bytes: tc.bytes, StreamFormat: "matroska"}
				for i := range 20 {
					s := stores[i%len(stores)]
					track := *first
					track.SHA256 = fmt.Sprintf("%064x", i+1)
					ctx, cancel := context.WithCancel(t.Context())
					done := make(chan error, 1)
					go func() {
						_, unpin, err := s.mirror(ctx, &track)
						if unpin != nil {
							unpin()
						}
						done <- err
					}()
					select {
					case <-blobs.entered:
						s.mu.Lock()
						admitted = append(admitted, s.mirrors[filepath.Join(s.mirrorDir(), mirrorName(&track))])
						s.mu.Unlock()
						cancel()
						if err := <-done; !errors.Is(err, context.Canceled) {
							t.Fatalf("canceled fetch waiter: %v", err)
						}
					case err := <-done:
						cancel()
						if !errors.Is(err, playback.ErrSubtitleReadsBusy) {
							t.Fatalf("fetch admission refusal: %v", err)
						}
					}
				}
				if len(admitted) != tc.limit {
					t.Errorf("admitted %d detached downloads reserving %d bytes, want %d", len(admitted), int64(len(admitted))*tc.bytes, tc.limit)
				}
				var temps int
				for _, s := range stores {
					files, _ := filepath.Glob(filepath.Join(s.mirrorDir(), mirrorTempPrefix+"*"))
					temps += len(files)
				}
				if temps != len(admitted) {
					t.Errorf("%d temporary files for %d admitted workers", temps, len(admitted))
				}
				// A warm track and another caller of an admitted key still work
				// when no new fetch can be admitted.
				s := stores[0]
				warm := &Track{SHA256: "warm", StreamFormat: "matroska"}
				if err := os.WriteFile(filepath.Join(s.mirrorDir(), mirrorName(warm)), []byte("warm"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, unpin, err := s.mirror(t.Context(), warm)
				if err != nil {
					t.Fatal(err)
				}
				unpin()
				joined := make(chan error, 1)
				go func() {
					_, unpin, err := s.mirror(t.Context(), first)
					if unpin != nil {
						unpin()
					}
					joined <- err
				}()
				synctest.Wait()
				unblock()
				if err := <-joined; !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Errorf("joining an admitted key: %v", err)
				}
				for _, f := range admitted {
					<-f.done
				}
				for _, s := range stores {
					files, _ := filepath.Glob(filepath.Join(s.mirrorDir(), mirrorTempPrefix+"*"))
					if len(files) != 0 {
						t.Errorf("failed fetches left %d temporary files", len(files))
					}
				}
				checkMirrorFetchRecovery(t, s)
			})
		})
	}
}

// Failure returns both worker and byte capacity; successful downloads return it too.
func checkMirrorFetchRecovery(t *testing.T, s *Store) {
	t.Helper()
	for i := range 10 {
		data := []byte(fmt.Sprintf("recovered %d", i))
		sum := sha256.Sum256(data)
		s.blobs = &memBlobs{objects: map[string][]byte{"recovered": data}}
		track := &Track{BlobKey: "recovered", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), StreamFormat: "matroska"}
		path, unpin, err := s.mirror(t.Context(), track)
		if err != nil {
			t.Fatalf("fetch after worker exit: %v", err)
		}
		unpin()
		if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
			t.Fatalf("recovered fetch: %q, %v", got, err)
		}
	}
}

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
		path, _, _, release, err := s.writeMirror(dir, ".mks", nil, func(w io.Writer) error {
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

// A node without a kept track's local copy fetches one only while fewer
// than maxMirrorFetches fetches run and copies being written leave room in
// the budget; otherwise it streams the object from storage, checked against
// its row, and writes no copy. Room for a fetch is made before it fetches.
func TestMirrorFetchesAreAdmitted(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	path, file := fixtureFile(t, ffmpeg, true)
	ref := *playback.EmbeddedSubtitleRefFor(file, 0)
	want := demux(t, ffmpeg, path, 0, 0, 0)
	// keptOnColdNode keeps the track, then drops the node's copies.
	keptOnColdNode := func(t *testing.T) (*Store, *memBlobs, *Track) {
		t.Helper()
		s, repo, blobs := newTestStore(t, ffmpeg)
		if _, err := s.WholeTrack(t.Context(), path, ref); err != nil {
			t.Fatal(err)
		}
		kept, _ := repo.Track(t.Context(), file.ID, 1)
		if err := os.RemoveAll(s.mirrorDir()); err != nil {
			t.Fatal(err)
		}
		return s, blobs, kept
	}
	for _, tc := range []struct {
		name   string
		full   func(*testing.T, *Store)
		tamper bool
	}{
		{name: "fetches running", full: fillMirrorFetches},
		{name: "room held by copies being written", full: func(t *testing.T, s *Store) { s.mirrorReserved = s.opts.MirrorMaxBytes }},
		{name: "tampered object", full: fillMirrorFetches, tamper: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, blobs, kept := keptOnColdNode(t)
			tc.full(t, s)
			if tc.tamper {
				blobs.objects[kept.BlobKey] = bytes.Repeat([]byte("x"), len(blobs.objects[kept.BlobKey]))
			}
			in, err := s.WholeTrack(t.Context(), path, ref)
			if err != nil {
				t.Fatal(err)
			}
			r, err := in.Stream()
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.ReadAll(r)
			_ = r.Close()
			if _, fromStorage := r.(*checkedObject); !fromStorage {
				t.Fatalf("the stream is a %T, not the object in storage", r)
			}
			switch {
			case tc.tamper:
				if readErr == nil || !strings.Contains(readErr.Error(), "SHA-256") {
					t.Fatalf("a tampered object was read: %v", readErr)
				}
			case readErr != nil:
				t.Fatal(readErr)
			default:
				if got := convert(t, ffmpeg, in); got != want {
					t.Fatalf("the object converts to %q, want %q", got, want)
				}
			}
			if entries, _ := os.ReadDir(s.mirrorDir()); len(entries) != 0 {
				t.Fatalf("a copy was written: %v", entries)
			}
		})
	}
	t.Run("room is made before fetching", func(t *testing.T) {
		s, blobs, kept := keptOnColdNode(t)
		if err := os.MkdirAll(s.mirrorDir(), 0o750); err != nil {
			t.Fatal(err)
		}
		old := filepath.Join(s.mirrorDir(), "old.mks")
		if err := os.WriteFile(old, make([]byte, 1000), 0o600); err != nil {
			t.Fatal(err)
		}
		// The old copy and the fetched one don't both fit.
		s.opts.MirrorMaxBytes = 1000 + kept.Bytes - 1
		var oldAtFetch atomic.Bool
		blobs.getHook = func() {
			_, err := os.Stat(old)
			oldAtFetch.Store(err == nil)
		}
		in, err := s.WholeTrack(t.Context(), path, ref)
		if err != nil {
			t.Fatal(err)
		}
		if got := convert(t, ffmpeg, in); got != want {
			t.Fatalf("the copy converts to %q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(s.mirrorDir(), mirrorName(kept))); err != nil || oldAtFetch.Load() {
			t.Fatalf("copy %v; old copy still there when the fetch began: %v", err, oldAtFetch.Load())
		}
	})
}

// fillMirrorFetches takes every fetch the process admits until the test ends.
func fillMirrorFetches(t *testing.T, _ *Store) {
	t.Helper()
	for range maxMirrorFetches {
		finish, err := reserveMirrorFetch(1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(finish)
	}
}
