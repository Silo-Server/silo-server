package nodemetrics

import (
	"context"
	"testing"
	"time"
)

// i915 names its engines render/video/video-enhance and reports one fd per
// context; ffmpeg dups the device fd, so the same drm-client-id shows up more
// than once with identical counters.
const i915Fdinfo = `pos:	0
flags:	02100002
mnt_id:	26
drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-client-id:	42
drm-engine-render:	1000000000 ns
drm-engine-copy:	500000000 ns
drm-engine-video:	2000000000 ns
drm-engine-video-enhance:	0 ns
`

// amdgpu names them gfx/enc/dec and prints a 32-bit-looking domain.
const amdgpuFdinfo = `pos:	0
drm-driver:	amdgpu
drm-pdev:	0000:03:00.0
drm-client-id:	7
drm-engine-gfx:	400000000 ns
drm-engine-enc0:	800000000 ns
drm-engine-dec0:	100000000 ns
`

// A non-DRM fd (a segment being written) must be ignored entirely.
const plainFdinfo = `pos:	4096
flags:	02100002
mnt_id:	26
`

func TestReadFdinfoCountersDeduplicatesClientsAndClassifiesEngines(t *testing.T) {
	tree := newProcTree(t)
	// Two fds on one i915 client, plus an unrelated file fd.
	tree.write("4242/fdinfo/3", i915Fdinfo)
	tree.write("4242/fdinfo/7", i915Fdinfo)
	tree.write("4242/fdinfo/9", plainFdinfo)
	// A second process on a different card.
	tree.write("4243/fdinfo/3", amdgpuFdinfo)

	clients := readFdinfoCounters(tree.root, []int{4242, 4243, 9999})
	if len(clients) != 2 {
		t.Fatalf("clients = %v, want one client per card", clients)
	}

	intel := clients[fdinfoClient{pdev: "0000:00:02.0", clientID: "42"}]
	// Counted once despite two fds; the copy engine is excluded.
	if intel.videoNS != 2_000_000_000 || intel.renderNS != 1_000_000_000 {
		t.Fatalf("i915 counters = %+v, want video 2e9 render 1e9 counted once", intel)
	}

	amd := clients[fdinfoClient{pdev: "0000:03:00.0", clientID: "7"}]
	if amd.renderNS != 400_000_000 {
		t.Fatalf("amdgpu render = %d, want gfx classified as render", amd.renderNS)
	}
	if amd.videoNS != 900_000_000 {
		t.Fatalf("amdgpu video = %d, want enc0+dec0 classified as video", amd.videoNS)
	}

	// Summing is the caller's job, and only after per-client deltas: these
	// counters are cumulative per client, so a device total is not diffable.
	deltas := deviceEngineDeltas(nil, clients)
	if got := deltas["0000:00:02.0"].videoNS; got != 2_000_000_000 {
		t.Fatalf("first-reading delta = %d, want the client's whole counter", got)
	}
}

// A driver that publishes no drm-client-id must still produce one stable
// identity per process and device, or the fd dups would multiply its busyness
// and the identity would change every time the client did work.
func TestReadFdinfoCountersKeysAnonymousClientsByProcess(t *testing.T) {
	anon := `drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-engine-video:	2000000000 ns
`
	tree := newProcTree(t)
	tree.write("4242/fdinfo/3", anon)
	tree.write("4242/fdinfo/7", anon)

	clients := readFdinfoCounters(tree.root, []int{4242})
	if len(clients) != 1 {
		t.Fatalf("clients = %v, want the two fds collapsed onto one client", clients)
	}
	for client, counters := range clients {
		if client.clientID != "anon:pid:4242" {
			t.Fatalf("clientID = %q, want an identity that survives the client doing work", client.clientID)
		}
		if counters.videoNS != 2_000_000_000 {
			t.Fatalf("videoNS = %d, want the dup counted once", counters.videoNS)
		}
	}
}

// PCI addresses arrive with different domain widths and cases depending on the
// source; they have to collapse to one key or a device is counted twice.
func TestNormalizePCIAddress(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0000:00:02.0", "0000:00:02.0"},
		{"00000000:03:00.0", "0000:03:00.0"},
		{"0000:03:00.0", "0000:03:00.0"},
		{" 00000000:0A:00.0 ", "0000:0a:00.0"},
		{"not-an-address", "not-an-address"},
	} {
		if got := NormalizePCIAddress(tc.in); got != tc.want {
			t.Fatalf("NormalizePCIAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSampleGPUMapsPdevToDevicePathAndComputesBusy(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	tree.write("4242/fdinfo/3", i915Fdinfo)

	s := newTestSampler(t, tree, clock, Options{
		FFmpegChildren: func() []int { return []int{4242} },
		DeviceSessions: func() map[string]int { return map[string]int{"/dev/dri/renderD128": 2} },
		DeviceIdentities: func() []DeviceIdentity {
			return []DeviceIdentity{{
				// nvidia-smi-style wide domain, to prove normalization is what
				// joins the two views rather than string equality.
				Path:       "/dev/dri/renderD128",
				PCIAddress: "00000000:00:02.0",
				Vendor:     "intel",
			}}
		},
	})

	s.sample(context.Background())
	first := s.Snapshot().GPU
	if len(first) != 1 {
		t.Fatalf("GPU = %+v, want one device", first)
	}
	if first[0].Device != "/dev/dri/renderD128" {
		t.Fatalf("Device = %q, want the render node path, not the PCI address", first[0].Device)
	}
	if first[0].Vendor != "intel" || first[0].Sessions != 2 {
		t.Fatalf("GPU[0] = %+v, want vendor intel and 2 sessions", first[0])
	}
	if first[0].Source != SourceFdinfo {
		t.Fatalf("Source = %q, want %q", first[0].Source, SourceFdinfo)
	}
	if first[0].VideoBusyPct != nil {
		t.Fatalf("VideoBusyPct = %d on the first sample, want it unset (nothing to diff against)", *first[0].VideoBusyPct)
	}

	// Over 4s of wall time: +2s of video engine, +1s of render engine.
	tree.write("4242/fdinfo/3", `drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-client-id:	42
drm-engine-render:	2000000000 ns
drm-engine-video:	4000000000 ns
`)
	clock.advance(4 * time.Second)
	s.sample(context.Background())

	second := s.Snapshot().GPU[0]
	if got := enginePct(t, second.VideoBusyPct); got != 50 {
		t.Fatalf("VideoBusyPct = %d, want 50", got)
	}
	if got := enginePct(t, second.RenderBusyPct); got != 25 {
		t.Fatalf("RenderBusyPct = %d, want 25", got)
	}
	if second.TotalBusyPct != nil {
		t.Fatal("TotalBusyPct set without an enrichment source")
	}
}

// One transcode exiting must not erase the work the others did in that
// interval. Their engine time is what the whole GPU panel is read from, and a
// node with normal session churn loses a client inside most intervals.
func TestSampleGPUKeepsSurvivingTranscodeBusyWhenAPeerExits(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	// Two transcodes on one card: the survivor at 2s of video engine time, the
	// one about to exit at 5s.
	tree.write("4242/fdinfo/3", i915Fdinfo)
	tree.write("4243/fdinfo/3", `drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-client-id:	43
drm-engine-video:	5000000000 ns
`)

	pids := []int{4242, 4243}
	s := newTestSampler(t, tree, clock, Options{
		FFmpegChildren: func() []int { return pids },
	})
	s.sample(context.Background())

	// The peer exits while the survivor saturates the video engine: +5s of
	// engine time over a 5s interval. The device total is 7s before and 7s
	// after, so a per-device baseline sees no gain and reports an idle GPU.
	pids = []int{4242}
	tree.write("4242/fdinfo/3", `drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-client-id:	42
drm-engine-video:	7000000000 ns
`)
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	if got := enginePct(t, s.Snapshot().GPU[0].VideoBusyPct); got != 100 {
		t.Fatalf("VideoBusyPct = %d while the surviving transcode ran the engine flat out, want 100", got)
	}
}

// A transcode exiting takes its accumulated engine time out of the device
// total, so the per-device sum falls. That is bookkeeping, not negative work.
func TestSampleGPUClampsNegativeDeltaWhenATranscodeExits(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")
	tree.write("4242/fdinfo/3", i915Fdinfo)
	tree.write("4243/fdinfo/3", `drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-client-id:	43
drm-engine-video:	5000000000 ns
`)

	pids := []int{4242, 4243}
	s := newTestSampler(t, tree, clock, Options{
		FFmpegChildren: func() []int { return pids },
	})
	s.sample(context.Background())

	// The second transcode exits; only the first process remains.
	pids = []int{4242}
	clock.advance(5 * time.Second)
	s.sample(context.Background())

	gpu := s.Snapshot().GPU
	if len(gpu) != 1 {
		t.Fatalf("GPU = %+v, want one device", gpu)
	}
	if video, render := enginePct(t, gpu[0].VideoBusyPct), enginePct(t, gpu[0].RenderBusyPct); video != 0 || render != 0 {
		t.Fatalf("busy = %d/%d after a transcode exited, want a measured 0/0", video, render)
	}

	// The reduced total is the new baseline: the surviving transcode's next
	// interval is measured against it, not against the pre-exit sum.
	tree.write("4242/fdinfo/3", `drm-driver:	i915
drm-pdev:	0000:00:02.0
drm-client-id:	42
drm-engine-video:	3000000000 ns
`)
	clock.advance(10 * time.Second)
	s.sample(context.Background())
	if got := enginePct(t, s.Snapshot().GPU[0].VideoBusyPct); got != 10 {
		t.Fatalf("VideoBusyPct after re-baselining = %d, want 10", got)
	}
}

// A device with no DRM counters and no enrichment still has to appear, so an
// operator sees the GPU exists — but it must say so rather than report zeros as
// a measurement.
func TestSampleGPUReportsKnownDeviceAsUnavailable(t *testing.T) {
	tree := newProcTree(t)
	clock := newFakeClock()
	tree.write("stat", "cpu  0 0 0 0 0 0 0 0\n")
	tree.write("loadavg", "0 0 0 0/0 0\n")
	tree.write("meminfo", "MemTotal: 1024 kB\n")
	tree.write("net/dev", "")

	s := newTestSampler(t, tree, clock, Options{
		DeviceIdentities: func() []DeviceIdentity {
			return []DeviceIdentity{{Path: "/dev/dri/renderD128", PCIAddress: "0000:00:02.0", Vendor: "intel"}}
		},
	})
	s.sample(context.Background())

	gpu := s.Snapshot().GPU
	if len(gpu) != 1 || gpu[0].Source != SourceUnavailable {
		t.Fatalf("GPU = %+v, want one device sourced %q", gpu, SourceUnavailable)
	}
}

func TestDefaultFFmpegChildrenMatchesOnlyFFmpegChildren(t *testing.T) {
	tree := newProcTree(t)
	tree.write("100/task/100/children", "200 300\n")
	tree.write("100/task/105/children", "400\n")
	tree.write("200/comm", "ffmpeg\n")
	// The kernel truncates comm to 15 bytes, which is why this is a substring
	// test and not an equality one.
	tree.write("300/comm", "ffmpeg-static-b\n")
	tree.write("400/comm", "postgres\n")

	pids := defaultFFmpegChildren(tree.root, 100)
	if len(pids) != 2 {
		t.Fatalf("pids = %v, want the two ffmpeg children", pids)
	}
	for _, pid := range pids {
		if pid == 400 {
			t.Fatalf("pids = %v, want the non-ffmpeg child excluded", pids)
		}
	}
}

func TestEngineBusyPercentClamps(t *testing.T) {
	elapsed := (5 * time.Second).Nanoseconds()
	if got := engineBusyPercent(10*uint64(elapsed), elapsed); got != 100 {
		t.Fatalf("busy over 100%% = %d, want clamped to 100", got)
	}
	if got := engineBusyPercent(0, elapsed); got != 0 {
		t.Fatalf("busy with no work = %d, want 0", got)
	}
	if got := engineBusyPercent(100, 0); got != 0 {
		t.Fatalf("busy with no elapsed time = %d, want 0", got)
	}
}

// Counters are only monotone per client. A client whose counter fell (a driver
// reset, or a reused client id) must contribute nothing rather than wrap.
func TestDeviceEngineDeltasIgnoresCounterRegressions(t *testing.T) {
	client := fdinfoClient{pdev: "0000:00:02.0", clientID: "42"}
	previous := map[fdinfoClient]engineCounters{client: {videoNS: 5_000_000_000}}
	current := map[fdinfoClient]engineCounters{client: {videoNS: 1_000_000_000}}
	if got := deviceEngineDeltas(previous, current)["0000:00:02.0"].videoNS; got != 0 {
		t.Fatalf("delta on a counter regression = %d, want 0", got)
	}
}

// enginePct reads an engine percentage a test requires to be present, so a
// missing measurement fails as itself rather than as a nil dereference.
func enginePct(t *testing.T, got *int) int {
	t.Helper()
	if got == nil {
		t.Fatal("engine percentage is unset, want a measurement")
	}
	return *got
}
