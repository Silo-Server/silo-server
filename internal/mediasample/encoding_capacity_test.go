package mediasample

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
)

func TestConcatCapacityFailureIsNotCached(t *testing.T) {
	f := newFakeListings()
	useListings(t, f)
	full := true
	capsCache.list = func(ctx context.Context, path string, stdin []byte, args ...string) ([]byte, error) {
		if stdin != nil && full {
			return nil, encodinglimit.ErrAtCapacity
		}
		return f.list(ctx, path, stdin, args...)
	}
	path := fakeBinary(t)
	if _, err := LoadCapabilities(t.Context(), path); !errors.Is(err, encodinglimit.ErrAtCapacity) {
		t.Errorf("capacity treated as unsupported concat: %v", err)
	}
	full = false
	caps, err := LoadCapabilities(t.Context(), path)
	if err != nil || !caps.concatSamples {
		t.Fatalf("free node did not retry concat: %+v, %v", caps, err)
	}
}

func TestHardwareResolverDoesNotCacheCapacityFailure(t *testing.T) {
	h := NewHardwareResolver("auto", "")
	calls := 0
	h.ResolveResult = func(context.Context, string, string, string) (string, error) {
		calls++
		if calls == 1 {
			return "none", encodinglimit.ErrAtCapacity
		}
		return "qsv", nil
	}
	h.Backend(t.Context(), "ffmpeg")
	if accel, _ := h.Backend(t.Context(), "ffmpeg"); accel != "qsv" || calls != 2 {
		t.Fatalf("temporary capacity failure cached: %s, %d calls", accel, calls)
	}
}
