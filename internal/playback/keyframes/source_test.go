package keyframes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync/atomic"
	"testing"
	"time"
)

// memorySource is a Source over contents whose opens are counted, and whose
// reads fail with failure, when set, as a relayed source's reader does: an
// *fs.PathError.
type memorySource struct {
	contents []byte
	opens    atomic.Int32
	failure  error
}

func (m *memorySource) source(ctx context.Context, identity string) Source {
	return Source{Identity: identity, Size: int64(len(m.contents)), Context: ctx, Open: m.open}
}

func (m *memorySource) open(context.Context) (ReaderAtCloser, error) {
	m.opens.Add(1)
	return memoryReader{Reader: bytes.NewReader(m.contents), failure: m.failure}, nil
}

type memoryReader struct {
	*bytes.Reader
	failure error
}

func (r memoryReader) ReadAt(p []byte, off int64) (int, error) {
	if r.failure != nil {
		return 0, &fs.PathError{Op: "read", Path: "source", Err: r.failure}
	}
	return r.Reader.ReadAt(p, off)
}

func (memoryReader) Close() error { return nil }

// identities numbers the source identities tests use. The index cache is the
// process's, so a fixed identity would find an earlier run's result
// (-count=N).
var identities atomic.Int64

// sourceIdentity is a source identity no other test or run has used. Once the
// test ends it waits for any background check of that identity to finish:
// the check runs within the test's context, which ends then.
func sourceIdentity(t *testing.T, name string) string {
	t.Helper()
	identity := fmt.Sprintf(`"file-%s-%d"`, name, identities.Add(1))
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			indexCache.Lock()
			running := false
			for key := range indexCache.inFlight {
				running = running || key.identity == identity
			}
			indexCache.Unlock()
			if !running {
				return
			}
			if time.Now().After(deadline) {
				t.Error("a background check outlived its test")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	return identity
}

// A Source is cached under its identity, whatever reads it: a second job on
// the same source revision, at another loopback URL, reads nothing.
func TestLoadSourceKeysOnIdentity(t *testing.T) {
	identity, otherIdentity := sourceIdentity(t, "keyed"), sourceIdentity(t, "keyed-other")
	first := &memorySource{contents: file(layout{})}
	got, err := LoadSourceVerified(t.Context(), first.source(t.Context(), identity))
	wantIndex(t, got, err)

	again := &memorySource{contents: file(layout{}), failure: errors.New("must not be read")}
	got, err = LoadSource(t.Context(), again.source(t.Context(), identity))
	wantIndex(t, got, err)
	if again.opens.Load() != 0 {
		t.Fatal("a cached source revision was read again")
	}

	// Another revision is another entry.
	other := &memorySource{contents: file(layout{})}
	if _, err := LoadSource(t.Context(), other.source(t.Context(), otherIdentity)); !errors.Is(err, ErrUnverified) || other.opens.Load() != 1 {
		t.Fatalf("another revision: err = %v after %d opens, want a fresh read", err, other.opens.Load())
	}
}

// A read that fails says nothing lasting about the source: the error comes
// back as a read failure, not ErrNoIndex, and isn't cached.
func TestLoadSourceTreatsReadFailuresAsTransient(t *testing.T) {
	for name, failure := range map[string]error{
		"budget":   errors.New("nodesource: source reader budget exceeded"),
		"deadline": context.DeadlineExceeded,
		"upstream": errors.New("nodesource: the core sent no source bytes within the retry window"),
	} {
		t.Run(name, func(t *testing.T) {
			identity := sourceIdentity(t, "transient-"+name)
			failing := &memorySource{contents: file(layout{}), failure: failure}
			_, err := LoadSourceVerified(t.Context(), failing.source(t.Context(), identity))
			if err == nil || errors.Is(err, ErrNoIndex) || !transient(err) || !errors.Is(err, failure) {
				t.Fatalf("verified load err = %v, want the transient read failure", err)
			}
			if _, err := LoadSource(t.Context(), failing.source(t.Context(), identity)); errors.Is(err, ErrNoIndex) || !transient(err) {
				t.Fatalf("quick check err = %v, want the transient read failure", err)
			}
			healthy := &memorySource{contents: file(layout{})}
			got, err := LoadSourceVerified(t.Context(), healthy.source(t.Context(), identity))
			wantIndex(t, got, err)
		})
	}
}

// The first LoadSource starts the full check in the background; a source
// released before it runs ends it uncached, and a later load checks again.
func TestLoadSourceVerifiesInTheBackground(t *testing.T) {
	identity, releasedIdentity := sourceIdentity(t, "background"), sourceIdentity(t, "background-released")
	src := &memorySource{contents: file(layout{})}
	if _, err := LoadSource(t.Context(), src.source(t.Context(), identity)); !errors.Is(err, ErrUnverified) {
		t.Fatalf("first LoadSource err = %v, want ErrUnverified", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := LoadSource(t.Context(), src.source(t.Context(), identity))
		if !errors.Is(err, ErrUnverified) {
			wantIndex(t, got, err)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background check didn't finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	released, release := context.WithCancel(t.Context())
	release()
	gone := &memorySource{contents: file(layout{})}
	if _, err := LoadSource(t.Context(), gone.source(released, releasedIdentity)); !transient(err) || errors.Is(err, ErrNoIndex) {
		t.Fatalf("released source err = %v, want a read failure", err)
	}
	if _, _, ok := cached(cacheKey{identity: releasedIdentity, size: int64(len(gone.contents))}); ok {
		t.Fatal("a released source's check was cached")
	}
}

// The full check reads through OpenSequential when the source has one; the
// quick check keeps Open's random access.
func TestFullCheckOpensTheSequentialReader(t *testing.T) {
	for _, wait := range []bool{true, false} {
		random := &memorySource{contents: file(layout{})}
		sequential := &memorySource{contents: file(layout{})}
		identity := sourceIdentity(t, "sequential-"+map[bool]string{true: "verified", false: "background"}[wait])
		source := random.source(t.Context(), identity)
		source.OpenSequential = sequential.open
		if wait {
			got, err := LoadSourceVerified(t.Context(), source)
			wantIndex(t, got, err)
			if random.opens.Load() != 0 || sequential.opens.Load() != 1 {
				t.Fatalf("verified load opened %d random, %d sequential readers", random.opens.Load(), sequential.opens.Load())
			}
			continue
		}
		if _, err := LoadSource(t.Context(), source); !errors.Is(err, ErrUnverified) {
			t.Fatalf("quick check = %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := LoadSource(t.Context(), source)
			if !errors.Is(err, ErrUnverified) {
				wantIndex(t, got, err)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the background check never finished")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if random.opens.Load() != 1 || sequential.opens.Load() != 1 {
			t.Fatalf("background check opened %d random (want the quick check's 1), %d sequential readers", random.opens.Load(), sequential.opens.Load())
		}
	}
}
