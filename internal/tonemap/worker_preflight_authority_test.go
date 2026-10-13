package tonemap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerinput"
)

func TestPreflightWarmCacheRequiresCurrentSourceAuthority(t *testing.T) {
	resetSourcePreflightCache(t)
	request := sourcePreflightTestRequest(t)
	var active atomic.Bool
	active.Store(true)
	adapter, err := workerinput.New(workerinput.Options{Origins: []string{"http://core.invalid"}, AllowHTTP: true, Allowed: func(string, uint64) bool { return active.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	source := workerinput.Source{ID: "media", Revision: "revision", URL: "http://core.invalid/source", Grant: "opaque", ETag: `"revision"`, Size: request.SourceRevision.FileSize, WorkID: "work", Generation: 1}
	handle, err := adapter.Open(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	request.InputPath = handle.URL()
	ctx := workerinput.WithContext(context.Background(), adapter)
	conversions := 0
	runner := sourcePreflightTestRunner(&conversions, func() string { return "ffmpeg version authority" }, nil)
	if err := ValidateSourceWithRunner(ctx, request, runner); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSourceWithRunner(ctx, request, runner); err != nil {
		t.Fatal(err)
	}
	if conversions != 3 {
		t.Fatalf("warm verdict did not reuse initial conversion: %d", conversions)
	}
	active.Store(false)
	if err := ValidateSourceWithRunner(ctx, request, runner); !errors.Is(err, ErrSourceRevisionChanged) {
		t.Fatalf("revoked cached source accepted: %v", err)
	}
	if conversions != 3 {
		t.Fatal("refused source executed another conversion")
	}
}
