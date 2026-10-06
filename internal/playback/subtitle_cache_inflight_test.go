package playback

import (
	"context"
	"io"
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
	// Release the first extraction only once the second request has found
	// it in progress, so the test exercises the shared fill, not a later
	// cache hit.
	busy := make(chan struct{})
	var busyOnce sync.Once
	afterBusyTextFill = func() { busyOnce.Do(func() { close(busy) }) }
	t.Cleanup(func() { afterBusyTextFill = nil })
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[1], errs[1] = cache.ExtractText(context.Background(), source, 0, "srt", extract)
	}()
	<-busy
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

// A fill that commits after ExtractText finds it in progress, but before
// ExtractText waits for it, is still reused: the next lookup finds it, and
// the source isn't demuxed again.
func TestExtractTextReusesAFillThatCommitsBeforeTheWait(t *testing.T) {
	cache, source := newTestCache(t)
	holder := cache.beginFill(source, 0, "srt")
	if holder == nil {
		t.Fatal("couldn't reserve the fill")
	}
	if _, err := holder.Tee(io.Discard).Write([]byte("SHARED")); err != nil {
		t.Fatal(err)
	}
	afterBusyTextFill = func() {
		afterBusyTextFill = nil
		if err := holder.Commit(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterBusyTextFill = nil })

	extracts := 0
	data, err := cache.ExtractText(t.Context(), source, 0, "srt", func(context.Context) ([]byte, error) {
		extracts++
		return []byte("AGAIN"), nil
	})
	if err != nil || string(data) != "SHARED" || extracts != 0 {
		t.Fatalf("ExtractText = %q, %v after %d extracts; want the committed fill and none", data, err, extracts)
	}
}

// Without a cache directory there's no fill to share: ExtractText extracts.
func TestExtractTextWithoutACacheExtracts(t *testing.T) {
	cache := NewSubtitleCache(func() string { return "" })
	extracts := 0
	data, err := cache.ExtractText(t.Context(), "/media/movie.mkv", 0, "srt", func(context.Context) ([]byte, error) {
		extracts++
		return []byte("DATA"), nil
	})
	if err != nil || string(data) != "DATA" || extracts != 1 {
		t.Fatalf("ExtractText = %q, %v after %d extracts; want one extract", data, err, extracts)
	}
}
