package keyframes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sourceCloser struct{}

func (sourceCloser) Close() error { return nil }
func indexedSource(t *testing.T, identity string) (Source, *atomic.Bool, *atomic.Int64) {
	t.Helper()
	data := file(layout{})
	var allowed atomic.Bool
	allowed.Store(true)
	var opens atomic.Int64
	return Source{Identity: identity, Size: int64(len(data)), Context: t.Context(), Validate: func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !allowed.Load() {
			return errors.New("revoked")
		}
		return nil
	}, OpenReader: func(context.Context) (io.ReaderAt, io.Closer, error) {
		opens.Add(1)
		return bytes.NewReader(data), sourceCloser{}, nil
	}}, &allowed, &opens
}
func TestLoadSourceVerifiedCachesRevisionButChecksCurrentAuthority(t *testing.T) {
	source, allowed, opens := indexedSource(t, t.Name()+":revision1")
	idx, err := LoadSourceVerified(t.Context(), source)
	wantIndex(t, idx, err)
	if _, err := LoadSourceVerified(t.Context(), source); err != nil || opens.Load() != 1 {
		t.Fatalf("cache opens=%d err=%v", opens.Load(), err)
	}
	allowed.Store(false)
	if _, err := LoadSourceVerified(t.Context(), source); err == nil || errors.Is(err, ErrNoIndex) || opens.Load() != 1 {
		t.Fatalf("revoked cache opens=%d err=%v", opens.Load(), err)
	}
	allowed.Store(true)
	source.Identity = t.Name() + ":revision2"
	if _, err := LoadSourceVerified(t.Context(), source); err != nil || opens.Load() != 2 {
		t.Fatalf("revision opens=%d err=%v", opens.Load(), err)
	}
}
func TestSourceReadFailuresDoNotPoisonIndexCache(t *testing.T) {
	source, _, opens := indexedSource(t, t.Name())
	original := source.OpenReader
	source.OpenReader = func(context.Context) (io.ReaderAt, io.Closer, error) { return nil, nil, context.DeadlineExceeded }
	if _, err := LoadSourceVerified(t.Context(), source); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNoIndex) {
		t.Fatalf("transient read cached as malformed: %v", err)
	}
	source.OpenReader = original
	idx, err := LoadSourceVerified(t.Context(), source)
	wantIndex(t, idx, err)
	if opens.Load() != 1 {
		t.Fatalf("recovery opens=%d", opens.Load())
	}
}

type canceledIndexReader struct {
	//nolint:containedctx // This admitted-reader fixture retains the owning context so reads observe cancellation.
	ctx     context.Context
	started chan struct{}
	once    sync.Once
}

func (r *canceledIndexReader) ReadAt([]byte, int64) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.ctx.Done()
	return 0, sourceFailure(r.ctx.Err())
}
func waitSourceCheck(t *testing.T, source Source) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	key := cacheKey{sourceIdentity: source.Identity, size: source.Size}
	for {
		indexCache.Lock()
		_, running := indexCache.inFlight[key]
		indexCache.Unlock()
		if !running {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("source background check did not stop")
		}
	}
}
func TestSourceBackgroundCheckIsBoundToSourceLifetime(t *testing.T) {
	source, _, _ := indexedSource(t, t.Name())
	original := source.OpenReader
	life, cancel := context.WithCancel(t.Context())
	defer cancel()
	source.Context = life
	started := make(chan struct{})
	var opens atomic.Int64
	source.OpenReader = func(ctx context.Context) (io.ReaderAt, io.Closer, error) {
		if opens.Add(1) == 1 {
			return original(ctx)
		}
		return &canceledIndexReader{ctx: ctx, started: started}, sourceCloser{}, nil
	}
	if _, err := LoadSource(t.Context(), source); !errors.Is(err, ErrUnverified) {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background source not opened")
	}
	cancel()
	waitSourceCheck(t, source)
	source.Context = t.Context()
	source.OpenReader = original
	idx, err := LoadSourceVerified(t.Context(), source)
	wantIndex(t, idx, err)
}
func TestSourceQueuedVerificationCancelsWithoutAcquiringSlot(t *testing.T) {
	verifySlots <- struct{}{}
	defer func() { <-verifySlots }()
	source, _, opens := indexedSource(t, t.Name())
	life, cancel := context.WithCancel(t.Context())
	defer cancel()
	source.Context = life
	if _, err := LoadSource(t.Context(), source); !errors.Is(err, ErrUnverified) {
		t.Fatal(err)
	}
	cancel()
	waitSourceCheck(t, source)
	if opens.Load() != 1 {
		t.Fatalf("canceled queue reopened source %d times", opens.Load())
	}
}
func TestSourceBackgroundOutlivesRequestButNotItsHandle(t *testing.T) {
	source, _, opens := indexedSource(t, t.Name())
	caller, cancel := context.WithCancel(t.Context())
	if _, err := LoadSource(caller, source); !errors.Is(err, ErrUnverified) {
		t.Fatal(err)
	}
	cancel()
	waitSourceCheck(t, source)
	idx, err := LoadSourceVerified(t.Context(), source)
	wantIndex(t, idx, err)
	if opens.Load() != 2 {
		t.Fatalf("background did not keep handle lifetime opens=%d", opens.Load())
	}
}
