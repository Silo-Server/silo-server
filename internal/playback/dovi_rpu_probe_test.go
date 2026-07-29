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

// The real failure exits 0: ffmpeg treats a per-packet bitstream-filter error
// as non-fatal and keeps running, so only stderr reveals that every packet was
// rejected. Trusting the exit code alone is what let a broken strip reach a
// live session.
func TestProbeOutputDetectsRejectedPackets(t *testing.T) {
	rejected := `[dovi_rpu @ 0x55] Failed to read unit 1 (type 39).
[dovi_rpu @ 0x55] Failed to read access unit from packet.
[vost#0:0/copy @ 0x55] Error applying bitstream filters to a packet: Invalid data found when processing input`

	if !dvRPUOutputFailed(rejected) {
		t.Fatal("a stream of rejected packets was read as success")
	}
	if dvRPUOutputFailed("") {
		t.Fatal("clean output was read as a failure")
	}
	if dvRPUOutputFailed("[hevc @ 0x55] Stream #0:0: Video: hevc") {
		t.Fatal("ordinary progress output was read as a failure")
	}
}

func writeProbeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "film.mkv")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write probe fixture: %v", err)
	}
	return path
}

// A file replaced in place keeps its path; inheriting the old verdict would
// keep stripping an RPU the new file cannot survive (or refuse one it can).
// Size alone misses a same-length re-encode, so the modification time counts
// too.
func TestProbeKeyChangesWithTheFile(t *testing.T) {
	path := writeProbeFile(t, "original")
	first, ok := dvRPUProbeKey("ffmpeg", path)
	if !ok {
		t.Fatal("a readable file produced no key")
	}
	again, _ := dvRPUProbeKey("ffmpeg", path)
	if again != first {
		t.Fatal("the same file produced two keys")
	}

	if err := os.WriteFile(path, []byte("replaced, same length"), 0o600); err != nil {
		t.Fatalf("replace probe fixture: %v", err)
	}
	if resized, _ := dvRPUProbeKey("ffmpeg", path); resized == first {
		t.Fatal("a replaced file reused the old verdict")
	}

	if sameFileOtherBinary, _ := dvRPUProbeKey("/opt/other/ffmpeg", path); sameFileOtherBinary == first {
		t.Fatal("a different ffmpeg build reused the old verdict")
	}
}

// A file that cannot be stat'd gets no key: the verdict belongs to whatever
// actually tries to open it, not to a probe that never ran.
func TestProbeKeyRefusesAnUnreadableFile(t *testing.T) {
	if _, ok := dvRPUProbeKey("ffmpeg", filepath.Join(t.TempDir(), "absent.mkv")); ok {
		t.Fatal("a missing file produced a cache key")
	}
}

// A nil probe must not change behaviour: most Profile 7 sources need the strip,
// so "no probe configured" has to mean "strip", not "never strip".
func TestNilProbeKeepsStripping(t *testing.T) {
	var probe *DVRPUProbe
	if !probe.CanStrip(context.Background(), "ffmpeg", "/media/film.mkv") {
		t.Fatal("a nil probe suppressed the strip")
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

// A stderr-confirmed rejection is the one verdict worth remembering.
func TestConclusiveVerdictsAreCachedAndReused(t *testing.T) {
	path := writeProbeFile(t, "not really a movie")
	probe := NewDVRPUProbe()
	key, ok := dvRPUProbeKey("ffmpeg", path)
	if !ok {
		t.Fatal("a readable file produced no key")
	}
	probe.mu.Lock()
	probe.results[key] = false
	probe.order = append(probe.order, key)
	probe.mu.Unlock()

	if probe.CanStrip(context.Background(), "ffmpeg", path) {
		t.Fatal("a cached rejection was ignored")
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
		"sleep 1\n" +
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

// The filter failing once per frame produced 376,316 stderr lines in the
// observed session; the markers are all in the first few, so the capture is
// bounded and the detection still works on the truncated head.
func TestProbeOutputCaptureIsBounded(t *testing.T) {
	buf := &cappedBuffer{limit: 64}
	head := "[dovi_rpu] Failed to read unit 1 (type 39).\n"
	if n, err := buf.Write([]byte(head)); n != len(head) || err != nil {
		t.Fatalf("short write reported to ffmpeg: %d %v", n, err)
	}
	flood := make([]byte, 1<<20)
	if n, err := buf.Write(flood); n != len(flood) || err != nil {
		t.Fatalf("short write reported to ffmpeg: %d %v", n, err)
	}
	if len(buf.buf) != 64 {
		t.Fatalf("capture exceeded its limit: %d bytes", len(buf.buf))
	}
	if !dvRPUOutputFailed(buf.String()) {
		t.Fatal("truncation lost the rejection the probe exists to spot")
	}
}
