package playback

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A verified backend is cached for the process lifetime, which is exactly the
// blind spot InvalidateHWProbeCache exists to close: without it, a driver
// upgraded underneath a running node can only be noticed by restarting. The
// observable contract is that ffmpeg is executed again, so this counts
// invocations of the fake binary rather than inspecting the cache.
func TestInvalidateHWProbeCacheForcesAnotherProbe(t *testing.T) {
	setupHWAccelTest(t)
	// This case counts commands rather than racing a deadline, so it opts out of
	// the shared fixture's very short probe timeout: a fake ffmpeg killed by a
	// loaded machine would fail here as a probe failure, which is not what is
	// under test. Restored by the fixture's own cleanup.
	hwProbeCommandTimeout = 5 * time.Second
	ffmpeg := writeFakeFFmpeg(t, successfulVAAPIProbe())

	probeCommands := func() int {
		data, err := os.ReadFile(ffmpeg.logPath)
		if err != nil {
			t.Fatalf("read probe log: %v", err)
		}
		return strings.Count(string(data), "\n")
	}

	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, "/dev/dri/renderD128"); !ok {
		t.Fatalf("first probe failed: %s", reason)
	}
	first := probeCommands()
	if first == 0 {
		t.Fatal("first probe ran no ffmpeg commands")
	}

	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, "/dev/dri/renderD128"); !ok {
		t.Fatalf("cached probe failed: %s", reason)
	}
	if got := probeCommands(); got != first {
		t.Fatalf("cached probe ran %d commands, want the cached verdict reused (%d)", got-first, first)
	}

	InvalidateHWProbeCache()

	if ok, reason := ffmpegSupportsBackend(transcodeHWVAAPI, ffmpeg.path, "/dev/dri/renderD128"); !ok {
		t.Fatalf("probe after invalidation failed: %s", reason)
	}
	if got := probeCommands(); got != first*2 {
		t.Fatalf("probe after invalidation ran %d commands total, want %d", got, first*2)
	}
}
