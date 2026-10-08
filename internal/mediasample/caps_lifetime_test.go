package mediasample

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awaitNoCapabilitiesInFlight(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if CapabilitiesInFlight() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("CapabilitiesInFlight() = %d after completing the shared inventory", CapabilitiesInFlight())
		}
		runtime.Gosched()
	}
}

func TestCapabilitiesLifetimeSurvivesCanceledCaller(t *testing.T) {
	for _, canceledBeforeAdmission := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			name := "cancel after command start/success"
			if canceledBeforeAdmission {
				name = "cancel before command admission/success"
			}
			if failed {
				name = strings.TrimSuffix(name, "success") + "command failure"
			}
			t.Run(name, func(t *testing.T) {
				awaitNoCapabilitiesInFlight(t)
				f := newFakeListings()
				useListings(t, f)
				binary := fakeBinary(t)
				started, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				t.Cleanup(func() {
					unblock()
					cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
					defer cleanupCancel()
					_, _ = LoadCapabilities(cleanupCtx, binary)
					awaitNoCapabilitiesInFlight(t)
				})
				var announce sync.Once
				var listings atomic.Int32
				var injectFailure atomic.Bool
				injectFailure.Store(failed)
				capsCache.Lock()
				capsCache.list = func(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
					listings.Add(1)
					if strings.Join(args, " ") == "-hide_banner -filters" {
						announce.Do(func() { close(started) })
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if injectFailure.Load() {
							return nil, errors.New("injected FFmpeg capability failure")
						}
					}
					return f.list(ctx, name, stdin, args...)
				}
				capsCache.Unlock()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if canceledBeforeAdmission {
					cancel()
				}
				done := make(chan error, 1)
				go func() {
					_, err := LoadCapabilities(ctx, binary)
					done <- err
				}()
				if !canceledBeforeAdmission {
					select {
					case <-started:
					case <-time.After(10 * time.Second):
						t.Fatal("capability inventory did not start its FFmpeg listing")
					}
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled capability caller returned %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("canceled capability caller kept its request handler blocked")
				}
				if got := CapabilitiesInFlight(); got < 1 {
					t.Fatalf("CapabilitiesInFlight() = %d while detached or pending FFmpeg listing has not returned", got)
				}
				unblock()
				awaitNoCapabilitiesInFlight(t)
				before := listings.Load()
				injectFailure.Store(false)
				caps, err := LoadCapabilities(t.Context(), binary)
				if err != nil || !caps.HasMuxer("chromaprint") {
					t.Fatalf("inventory after detached completion = %+v, %v", caps, err)
				}
				awaitNoCapabilitiesInFlight(t)
				if failed && listings.Load() != before+4 {
					t.Fatalf("failed inventory was not retried: listings=%d, before=%d", listings.Load(), before)
				}
				if !failed && listings.Load() != before {
					t.Fatal("successful detached inventory was not cached")
				}
			})
		}
	}
}

func TestCapabilitiesLifetimeSurvivesCacheInvalidation(t *testing.T) {
	awaitNoCapabilitiesInFlight(t)
	f := newFakeListings()
	useListings(t, f)
	binary := fakeBinary(t)
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var announce sync.Once
	var listings atomic.Int32
	capsCache.Lock()
	capsCache.list = func(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
		listings.Add(1)
		if strings.Join(args, " ") == "-hide_banner -filters" {
			announce.Do(func() { close(started) })
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return f.list(ctx, name, stdin, args...)
	}
	capsCache.Unlock()
	t.Cleanup(func() {
		unblock()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cleanupCancel()
		_, _ = LoadCapabilities(cleanupCtx, binary)
		awaitNoCapabilitiesInFlight(t)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := LoadCapabilities(ctx, binary)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("capability inventory did not start its listing")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled caller returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled caller remained blocked")
	}
	InvalidateCapabilities()
	if got := CapabilitiesInFlight(); got < 1 {
		t.Fatalf("CapabilitiesInFlight() = %d after invalidating a still-running inventory", got)
	}
	unblock()
	awaitNoCapabilitiesInFlight(t)
	before := listings.Load()
	if _, err := LoadCapabilities(t.Context(), binary); err != nil {
		t.Fatalf("inventory after invalidation: %v", err)
	}
	awaitNoCapabilitiesInFlight(t)
	if got := listings.Load(); got != before+4 {
		t.Fatalf("invalidated inventory reused an older generation: listings=%d, before=%d", got, before)
	}
}
