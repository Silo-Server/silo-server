package playback

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestExtractTextSharesInFlightFill: a request for a track another request is
// already extracting waits for that fill instead of demuxing the source again.
func TestExtractTextSharesInFlightFill(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := NewSubtitleCache(func() string { return filepath.Join(dir, "transcode") })
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	extract := func(context.Context) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), nil
	}

	var wg sync.WaitGroup
	results := make([][]byte, 2)
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = cache.ExtractText(context.Background(), source, 0, "srt", extract)
	}()
	<-started
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[1], errs[1] = cache.ExtractText(context.Background(), source, 0, "srt", extract)
	}()
	// The second request is either waiting on the fill or has not reached
	// ExtractText yet; neither may start a second extraction.
	close(release)
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if string(results[i]) != string(results[0]) {
			t.Fatalf("request %d read %q, want %q", i, results[i], results[0])
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("extract ran %d times, want 1", got)
	}
}
