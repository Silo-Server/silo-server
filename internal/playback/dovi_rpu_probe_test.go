package playback

import (
	"context"
	"testing"
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

// A file replaced in place keeps its path; inheriting the old verdict would
// keep stripping an RPU the new file cannot survive (or refuse one it can).
func TestProbeKeyChangesWithTheFile(t *testing.T) {
	same := DVRPUProbeKey("/media/film.mkv", 8_000_000_000)
	if DVRPUProbeKey("/media/film.mkv", 8_000_000_000) != same {
		t.Fatal("the same file produced two keys")
	}
	if DVRPUProbeKey("/media/film.mkv", 9_000_000_000) == same {
		t.Fatal("a replaced file reused the old verdict")
	}
}

// A nil probe must not change behaviour: most Profile 7 sources need the strip,
// so "no probe configured" has to mean "strip", not "never strip".
func TestNilProbeKeepsStripping(t *testing.T) {
	var probe *DVRPUProbe
	if !probe.CanStrip(context.Background(), "k", "/media/film.mkv") {
		t.Fatal("a nil probe suppressed the strip")
	}
}

func TestProbeRefusesAnEmptyPath(t *testing.T) {
	probe := NewDVRPUProbe(func() string { return "" })
	if !probe.CanStrip(context.Background(), "k", "  ") {
		t.Fatal("an unprobeable input should fall back to stripping")
	}
}

// A long-lived server must not accumulate one entry per file it has ever
// played.
func TestProbeCacheIsBounded(t *testing.T) {
	probe := NewDVRPUProbe(func() string { return "" })
	for i := range maxDVRPUProbeEntries + 5 {
		key := DVRPUProbeKey("/media/film.mkv", int64(i))
		probe.results[key] = true
		probe.order = append(probe.order, key)
	}
	probe.trimLocked()

	if len(probe.results) != maxDVRPUProbeEntries || len(probe.order) != maxDVRPUProbeEntries {
		t.Fatalf("probe cache grew unbounded: %d", len(probe.results))
	}
	if _, ok := probe.results[DVRPUProbeKey("/media/film.mkv", 0)]; ok {
		t.Fatal("the oldest entry survived eviction")
	}
}
