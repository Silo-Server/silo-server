package playback

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeProbeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "film.mkv")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write probe fixture: %v", err)
	}
	return path
}

// A file that cannot be stat'd gets no key: the verdict belongs to whatever
// actually tries to open it, not to a probe that never ran.
func TestProbeKeyRefusesAnUnreadableFile(t *testing.T) {
	if _, ok := dvRPUProbeKey("ffmpeg", filepath.Join(t.TempDir(), "absent.mkv")); ok {
		t.Fatal("a missing file produced a cache key")
	}
}

func TestProbeRefusesAnEmptyPath(t *testing.T) {
	probe := NewDVRPUProbe()
	if !probe.CanStrip(context.Background(), "ffmpeg", "  ") {
		t.Fatal("an unprobeable input should fall back to stripping")
	}
}

// Anything that stops the probe from reaching a conclusion — a cancelled
// request, a cold mount that outruns the timeout, an ffmpeg that will not
// start — must leave the strip on and leave the cache empty. Caching it would
// disable the strip for that file for every later viewer, which is the
// opposite of the nil-probe rule above.
func TestInconclusiveProbeIsNeitherFatalNorCached(t *testing.T) {
	path := writeProbeFile(t, "not really a movie")
	probe := NewDVRPUProbe()

	// A binary that does not exist fails to start: no verdict on the file.
	if !probe.CanStrip(context.Background(), filepath.Join(t.TempDir(), "no-such-ffmpeg"), path) {
		t.Fatal("an ffmpeg that could not run suppressed the strip")
	}
	probe.mu.Lock()
	cached := len(probe.results)
	probe.mu.Unlock()
	if cached != 0 {
		t.Fatalf("an inconclusive probe was cached: %d entries", cached)
	}
}

// The leader probes on behalf of every follower queued behind it, so its own
// client giving up must not abandon the run: the verdict is still reached and
// still cached for the next start. While the probe rode the caller's context,
// one client disconnecting turned the answer its neighbours were waiting on
// into an inconclusive fail-open and hung them on the very source it had been
// about to reject.
func TestProbeSurvivesTheLeaderLeaving(t *testing.T) {
	path := writeProbeFile(t, "not really a movie")
	bin, runLog := writeRejectingFFmpeg(t)
	probe := NewDVRPUProbe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if probe.CanStrip(ctx, bin, path) {
		t.Fatal("a cancelled caller abandoned the probe and fell back to stripping")
	}
	if runs := countRuns(t, runLog); runs != 1 {
		t.Fatalf("the probe ran %d times, want 1", runs)
	}
	probe.mu.Lock()
	cached := len(probe.results)
	probe.mu.Unlock()
	if cached != 1 {
		t.Fatalf("the verdict was not cached for the next start: %d entries", cached)
	}
}

// writeRejectingFFmpeg stands in for the real failure: it exits 0 while
// printing the dovi_rpu rejection, and records every invocation so a test can
// tell how many probes actually ran.
func writeRejectingFFmpeg(t *testing.T) (bin, runLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "ffmpeg")
	runLog = filepath.Join(dir, "runs")
	script := "#!/bin/sh\n" +
		"echo run >> " + runLog + "\n" +
		"sleep 0.2\n" +
		"echo '[dovi_rpu @ 0x55] Failed to read unit 1 (type 39).' >&2\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return bin, runLog
}

func countRuns(t *testing.T, runLog string) int {
	t.Helper()
	contents, err := os.ReadFile(runLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	return len(strings.Fields(string(contents)))
}

// Concurrent starts of the same title (a retry, a second household profile)
// must share one ffmpeg rather than each spawning a full probe, and all of them
// must come back with the one verdict it reached.
func TestConcurrentProbesShareOneRun(t *testing.T) {
	path := writeProbeFile(t, "not really a movie")
	bin, runLog := writeRejectingFFmpeg(t)
	probe := NewDVRPUProbe()

	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = probe.CanStrip(context.Background(), bin, path)
		}(i)
	}
	wg.Wait()

	for i, strippable := range results {
		if strippable {
			t.Fatalf("caller %d missed the rejection the probe found", i)
		}
	}
	if runs := countRuns(t, runLog); runs != 1 {
		t.Fatalf("the same source was probed %d times, want 1", runs)
	}

	// The verdict is conclusive, so a later start must reuse it rather than
	// pay for the probe again.
	if probe.CanStrip(context.Background(), bin, path) {
		t.Fatal("the cached rejection was ignored")
	}
	if runs := countRuns(t, runLog); runs != 1 {
		t.Fatalf("a cached verdict still spawned ffmpeg: %d runs", runs)
	}
}

// A follower whose own request is cancelled must not block on the leader.
func TestFollowerLeavesWhenItsRequestIsCancelled(t *testing.T) {
	path := writeProbeFile(t, "not really a movie")
	probe := NewDVRPUProbe()
	key, _ := dvRPUProbeKey("ffmpeg", path)
	probe.mu.Lock()
	probe.inflight[key] = &dvRPUCall{done: make(chan struct{}), strippable: false}
	probe.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan bool, 1)
	go func() { done <- probe.CanStrip(ctx, "ffmpeg", path) }()

	select {
	case strippable := <-done:
		if !strippable {
			t.Fatal("a cancelled follower reported a verdict it never waited for")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled follower blocked on the leader's probe")
	}
}

// A long-lived server must not accumulate one entry per file it has ever
// played.
func TestProbeCacheIsBounded(t *testing.T) {
	probe := NewDVRPUProbe()
	oldest := "ffmpeg|/media/film.mkv|0|0"
	for i := range maxDVRPUProbeEntries + 5 {
		key := "ffmpeg|/media/film.mkv|" + strconv.Itoa(i) + "|0"
		probe.results[key] = true
		probe.order = append(probe.order, key)
	}
	probe.trimLocked()

	if len(probe.results) != maxDVRPUProbeEntries || len(probe.order) != maxDVRPUProbeEntries {
		t.Fatalf("probe cache grew unbounded: %d", len(probe.results))
	}
	if _, ok := probe.results[oldest]; ok {
		t.Fatal("the oldest entry survived eviction")
	}
}
