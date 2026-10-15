package keyframes

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoadCachesUntilTheFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mkv")
	contents := file(layout{})
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	got, err := LoadVerified(t.Context(), path)
	wantIndex(t, got, err)

	// Same size and time with different bytes: still the cached index.
	damaged := slices.Clone(contents)
	for i := range damaged[:8] {
		damaged[i] = 0
	}
	if err := os.WriteFile(path, damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	got, err = Load(t.Context(), path)
	wantIndex(t, got, err)

	// A new modification time reads the file again.
	later := stamp.Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex for the changed file", err)
	}
}

func TestLoadReportsOtherFilesAsUnindexed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(path, []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex", err)
	}
	if _, err := Load(t.Context(), filepath.Join(t.TempDir(), "missing.mkv")); err == nil || errors.Is(err, ErrNoIndex) {
		t.Fatalf("missing file err = %v, want a read error", err)
	}
}

// The first Load starts the full check and doesn't use the index until it
// passes.
func TestLoadVerifiesInTheBackground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrUnverified) || !errors.Is(err, ErrNoIndex) {
		t.Fatalf("first Load err = %v, want ErrUnverified (an ErrNoIndex)", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := Load(t.Context(), path)
		if !errors.Is(err, ErrUnverified) {
			wantIndex(t, got, err)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background check didn't finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A malformed file is a lasting fact, cached like a missing index, not
// checked again on every playback.
func TestLoadCachesMalformedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.mkv")
	// Cues pointing at an element that isn't a cluster: with the Cues first,
	// cluster B ends the file.
	contents := file(layout{cuesFirst: true})
	broken := slices.Clone(contents)
	copy(broken[len(broken)-len(clusterB):], bytes.Repeat([]byte{0xEC}, 8)) // overwrite cluster B's header with Void
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerified(t.Context(), path); !errors.Is(err, ErrNoIndex) {
		t.Fatalf("malformed file err = %v, want ErrNoIndex", err)
	}
	// Repaired in place with the same size and time: still the cached result.
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrNoIndex) || errors.Is(err, ErrUnverified) {
		t.Fatalf("second load err = %v, want the cached ErrNoIndex", err)
	}
}

// A full check whose caller has gone stops with the caller's error, which
// isn't cached: the next check reads the file.
func TestLoadVerifiedStopsWhenCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canceled.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := LoadVerified(gone, path); !errors.Is(err, context.Canceled) || errors.Is(err, ErrNoIndex) {
		t.Fatalf("canceled check: err = %v, want context.Canceled and not ErrNoIndex", err)
	}
	got, err := LoadVerified(t.Context(), path)
	wantIndex(t, got, err)
}

// holdSlot keeps the full-check slot busy, so a queued check waits for it,
// until the returned release is called.
func holdSlot(t *testing.T) (release func()) {
	t.Helper()
	verifySlots <- struct{}{}
	var once bool
	release = func() {
		if !once {
			once = true
			<-verifySlots
		}
	}
	t.Cleanup(release)
	return release
}

func checksQueued() int {
	indexCache.Lock()
	defer indexCache.Unlock()
	return len(indexCache.inFlight)
}

// A background check stops once the playback that wanted it has gone, and
// isn't cached: the next playback starts it again (#2453).
func TestLoadStopsTheCheckWhenNoPlaybackHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stopped.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	release := holdSlot(t)
	session, stop := context.WithCancel(t.Context())
	if _, err := Load(session, path); !errors.Is(err, ErrUnverified) {
		t.Fatalf("first Load err = %v, want ErrUnverified", err)
	}
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for checksQueued() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the check kept waiting after its playback stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()

	// Nothing was cached, so a new playback queues the check again.
	if _, err := Load(t.Context(), path); !errors.Is(err, ErrUnverified) {
		t.Fatalf("Load after the stop err = %v, want ErrUnverified", err)
	}
	got, err := LoadVerified(t.Context(), path)
	wantIndex(t, got, err)
}

// A check that two playbacks want keeps going when one of them stops.
func TestLoadKeepsTheCheckWhileAnotherPlaybackHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.mkv")
	if err := os.WriteFile(path, file(layout{}), 0o644); err != nil {
		t.Fatal(err)
	}
	release := holdSlot(t)
	first, stopFirst := context.WithCancel(t.Context())
	second, stopSecond := context.WithCancel(t.Context())
	defer stopSecond()
	for _, session := range []context.Context{first, second} {
		if _, err := Load(session, path); !errors.Is(err, ErrUnverified) {
			t.Fatalf("Load err = %v, want ErrUnverified", err)
		}
	}
	stopFirst()
	time.Sleep(50 * time.Millisecond) // let the stop land, if it would
	if checksQueued() != 1 {
		t.Fatal("the check stopped while a playback still held it")
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := Load(second, path)
		if !errors.Is(err, ErrUnverified) {
			wantIndex(t, got, err)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background check didn't finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
