package nodemetrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DRM fdinfo baseline.
//
// Every process holding a DRM device publishes per-engine nanosecond counters
// under /proc/<pid>/fdinfo/<fd>. This needs no capabilities, no extra binaries
// and no driver-specific library, and it measures exactly the thing worth
// measuring on a transcode node: how much GPU engine time *our* ffmpeg children
// are consuming, per device. Its one blind spot is other tenants — a GPU shared
// with something outside this process looks idle here — which is what the
// nvidia-smi enrichment exists to cover where it can.
//
// Field names differ by driver (i915, xe, amdgpu), so engines are classified by
// name shape rather than matched against a fixed per-driver table: a driver we
// have never seen still reports usefully as long as it follows the DRM fdinfo
// convention.

// engineClass distinguishes the two engine groups worth separating for a media
// workload: fixed-function video (encode/decode) and everything general-purpose.
type engineClass int

const (
	engineOther engineClass = iota
	engineVideo
	engineRender
)

const (
	fdinfoEnginePrefix = "drm-engine-"
	fdinfoPdevKey      = "drm-pdev"
	fdinfoClientIDKey  = "drm-client-id"
)

// classifyEngine maps a drm-engine-* field name to its class.
//
//   - i915 reports render / video / video-enhance / copy
//   - xe reports rcs / vcs / vecs / bcs / ccs
//   - amdgpu reports gfx / compute / enc / dec / jpeg / vcn
func classifyEngine(field string) engineClass {
	name, ok := strings.CutPrefix(field, fdinfoEnginePrefix)
	if !ok {
		return engineOther
	}
	name = strings.ToLower(name)
	switch {
	case strings.HasPrefix(name, "video"), strings.HasPrefix(name, "enc"),
		strings.HasPrefix(name, "dec"), strings.HasPrefix(name, "vcn"),
		strings.HasPrefix(name, "jpeg"), strings.HasPrefix(name, "vcs"),
		strings.HasPrefix(name, "vecs"):
		return engineVideo
	case strings.HasPrefix(name, "render"), strings.HasPrefix(name, "gfx"),
		strings.HasPrefix(name, "compute"), strings.HasPrefix(name, "rcs"),
		strings.HasPrefix(name, "ccs"):
		return engineRender
	default:
		// Copy/blitter engines and anything unrecognized: real work, but not
		// work a video pipeline's headroom is judged by.
		return engineOther
	}
}

// engineCounters is cumulative engine time for one device.
type engineCounters struct {
	videoNS  uint64
	renderNS uint64
}

// fdinfoClient is one DRM client (a drm-client-id on a pdev). A process holds
// the same client on several fds — ffmpeg dups its device fd across filter and
// encoder contexts — and each fd reports the client's full counters, so
// counting per fd would multiply one GPU's busyness by its fd count.
type fdinfoClient struct {
	pdev     string
	clientID string
}

// readFdinfoCounters reads cumulative engine time per DRM client across the
// given processes, deduplicating the fds one client is held on.
//
// Counters stay per client rather than being summed per device here, because
// only a client's own counter is monotone: a device total falls whenever one of
// several clients exits, and a caller diffing that total would read the drop as
// negative work for every transcode still running on the card. Summing is the
// caller's job, after it has taken per-client deltas — see deviceEngineDeltas.
func readFdinfoCounters(procDir string, pids []int) map[fdinfoClient]engineCounters {
	clients := make(map[fdinfoClient]engineCounters)
	for _, pid := range pids {
		dir := filepath.Join(procDir, strconv.Itoa(pid), "fdinfo")
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A transcode that exited between listing and reading is the normal
			// case, not an error worth reporting.
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			client, counters, ok := parseFdinfo(pid, string(raw))
			if !ok {
				continue
			}
			// Highest wins: two fds on one client report the same counters, and
			// a read that raced an update reports slightly less.
			existing := clients[client]
			if counters.videoNS > existing.videoNS {
				existing.videoNS = counters.videoNS
			}
			if counters.renderNS > existing.renderNS {
				existing.renderNS = counters.renderNS
			}
			clients[client] = existing
		}
	}
	return clients
}

// deviceEngineDeltas converts two per-client readings into the engine time each
// device accumulated between them.
//
// A client absent from the previous reading contributes its whole counter: the
// only clients read here are ffmpeg children this process spawned, so a client
// that was not there last pass started during this interval and every
// nanosecond on its counter was earned inside it.
//
// A client that vanished contributes nothing, and — the point of doing this per
// client — costs the device nothing either. The device sum drops when a
// transcode exits, so diffing sums would report zero busy for the whole card
// while its surviving transcodes ran flat out.
func deviceEngineDeltas(previous, current map[fdinfoClient]engineCounters) map[string]engineCounters {
	deltas := make(map[string]engineCounters, len(current))
	for client, counters := range current {
		before := previous[client]
		delta := deltas[client.pdev]
		// Counters are monotone per client, so a fall is a driver reset or a
		// reused client id, not negative work.
		if counters.videoNS > before.videoNS {
			delta.videoNS += counters.videoNS - before.videoNS
		}
		if counters.renderNS > before.renderNS {
			delta.renderNS += counters.renderNS - before.renderNS
		}
		deltas[client.pdev] = delta
	}
	return deltas
}

// parseFdinfo reads one /proc/<pid>/fdinfo/<fd> file. Only DRM fds carry
// drm-pdev; everything else (sockets, files, the transcode output itself) is
// rejected by the missing key.
func parseFdinfo(pid int, content string) (fdinfoClient, engineCounters, bool) {
	var client fdinfoClient
	var counters engineCounters
	for line := range strings.Lines(content) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case fdinfoPdevKey:
			client.pdev = NormalizePCIAddress(value)
		case fdinfoClientIDKey:
			client.clientID = value
		default:
			class := classifyEngine(key)
			if class == engineOther {
				continue
			}
			// "12345678 ns" — the unit column is part of the convention.
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			ns, err := strconv.ParseUint(fields[0], 10, 64)
			if err != nil {
				continue
			}
			if class == engineVideo {
				counters.videoNS += ns
			} else {
				counters.renderNS += ns
			}
		}
	}
	if client.pdev == "" {
		return fdinfoClient{}, engineCounters{}, false
	}
	if client.clientID == "" {
		// A driver that publishes no drm-client-id leaves one bucket per process
		// per device, merged by highest counter. Keying by fd instead would
		// multiply a GPU's busyness by the fd count (ffmpeg dups its device fd),
		// and keying by the counter values would mint a new identity every time
		// the client did work — which is precisely the identity the interval
		// deltas have to follow. Two genuinely distinct anonymous clients in one
		// process collapse to the busier of the two; that undercounts, which is
		// the safe direction and only affects drivers old enough to omit the id.
		client.clientID = "anon:pid:" + strconv.Itoa(pid)
	}
	return client, counters, true
}

// engineBusyPercent converts an engine-time delta into a busy percentage over
// the elapsed wall time.
//
// The delta is already per-device work done in the interval (deviceEngineDeltas
// takes it per client, so client churn never produces a negative one). It can
// exceed the interval on a device with several engines of one class running
// concurrently, which clamps to 100 rather than reporting an impossible figure.
func engineBusyPercent(deltaNS uint64, elapsedNS int64) int {
	if elapsedNS <= 0 || deltaNS == 0 {
		return 0
	}
	return clampPercent(int(deltaNS * 100 / uint64(elapsedNS)))
}

// defaultFFmpegChildren returns the pids of this process's direct children that
// are ffmpeg.
//
// Direct children only, deliberately: those are the processes this node
// spawned and is accountable for. Walking the whole process table would pick up
// every other tenant's encoder on a shared host and report their GPU time as
// ours.
func defaultFFmpegChildren(procDir string, pid int) []int {
	taskDir := filepath.Join(procDir, strconv.Itoa(pid), "task")
	tasks, err := os.ReadDir(taskDir)
	if err != nil {
		return nil
	}
	seen := make(map[int]bool)
	var pids []int
	for _, task := range tasks {
		raw, err := os.ReadFile(filepath.Join(taskDir, task.Name(), "children"))
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(raw)) {
			child, err := strconv.Atoi(field)
			if err != nil || seen[child] {
				continue
			}
			seen[child] = true
			if !processIsFFmpeg(procDir, child) {
				continue
			}
			pids = append(pids, child)
		}
	}
	return pids
}

// processIsFFmpeg reports whether a pid's comm names an ffmpeg binary. comm is
// truncated to 15 bytes by the kernel, so this is a substring test rather than
// an equality one.
func processIsFFmpeg(procDir string, pid int) bool {
	raw, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(string(raw))), "ffmpeg")
}

// NormalizePCIAddress makes PCI addresses from different sources comparable.
// DRM fdinfo prints a 16-bit domain (0000:03:00.0), nvidia-smi a 32-bit one
// (00000000:03:00.0), and neither guarantees a case.
func NormalizePCIAddress(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	domain, rest, ok := strings.Cut(address, ":")
	if !ok {
		return address
	}
	value, err := strconv.ParseUint(domain, 16, 64)
	if err != nil {
		return address
	}
	return fmt.Sprintf("%04x:%s", value, rest)
}
