package playback

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Some Dolby Vision Profile 7 files carry an RPU that ffmpeg's dovi_rpu
// bitstream filter cannot parse. The filter does not fail cleanly: it rejects
// every packet, and ffmpeg keeps going, emitting a pair of errors per frame —
// one observed session produced 376,316 stderr lines before the process was
// killed. Playback never starts, the manifest build fails, and the client is
// handed a 503 after ~10 seconds, which it shows as an endless spinner:
//
//	[dovi_rpu] Failed to read unit 1 (type 39).
//	[vost#0:0/copy] Error applying bitstream filters to a packet:
//	    Invalid data found ... Invalid SEI message: payload_size too large
//
// Whether a given file survives the strip is a property of that file, not of
// ffmpeg, so it cannot be answered by SupportsDoviRPUFilter. It can be answered
// in about a second by asking ffmpeg to strip a couple of seconds to nowhere.
const (
	// Enough packets to hit the RPU; a file that survives this survives the run.
	dvRPUProbeSeconds = 2
	dvRPUProbeTimeout = 15 * time.Second
	// Same reasoning as the letterbox cache: bounded so a long-lived server with
	// a large library cannot accumulate an entry per file forever.
	maxDVRPUProbeEntries = 4096
)

// DVRPUProbe records, per source file, whether the Profile 7 RPU strip works.
type DVRPUProbe struct {
	ffmpegPath func() string

	mu      sync.Mutex
	results map[string]bool
	order   []string
}

func NewDVRPUProbe(ffmpegPath func() string) *DVRPUProbe {
	return &DVRPUProbe{
		ffmpegPath: ffmpegPath,
		results:    make(map[string]bool),
	}
}

// DVRPUProbeKey identifies a probe result. Keyed on size as well as path so a
// file replaced in place is re-probed rather than inheriting the old verdict.
func DVRPUProbeKey(inputPath string, fileSize int64) string {
	return inputPath + "|" + strconv.FormatInt(fileSize, 10)
}

// CanStrip reports whether the RPU strip should be attempted for this source.
//
// Unknown sources are probed inline — this runs on the playback-start path,
// where a second of certainty is worth far more than handing the client a
// stream that cannot start. Only Profile 7 sources ever reach here, so the
// cost lands on a small minority of titles, once each.
func (p *DVRPUProbe) CanStrip(ctx context.Context, key, inputPath string) bool {
	if p == nil || strings.TrimSpace(inputPath) == "" {
		// No probe configured: keep the previous behaviour rather than silently
		// dropping a strip that most files need.
		return true
	}

	p.mu.Lock()
	if result, ok := p.results[key]; ok {
		p.mu.Unlock()
		return result
	}
	p.mu.Unlock()

	started := time.Now()
	ok := runDVRPUProbe(ctx, p.binary(), inputPath)
	slog.Info("dolby vision rpu strip probed",
		"component", "playback",
		"input", inputPath,
		"can_strip", ok,
		"took_ms", time.Since(started).Milliseconds(),
	)

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.results[key]; !exists {
		p.order = append(p.order, key)
	}
	p.results[key] = ok
	p.trimLocked()
	return ok
}

// trimLocked bounds the cache, oldest first. A wrong eviction costs one extra
// probe of a file nobody has played in a long time.
func (p *DVRPUProbe) trimLocked() {
	for len(p.order) > maxDVRPUProbeEntries {
		delete(p.results, p.order[0])
		p.order = p.order[1:]
	}
}

func (p *DVRPUProbe) binary() string {
	path := ""
	if p.ffmpegPath != nil {
		path = p.ffmpegPath()
	}
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		return trimmed
	}
	return "ffmpeg"
}

// runDVRPUProbe strips a short head of the file to the null muxer. Any failure
// — a non-zero exit, or the filter's own rejection on stderr — means the strip
// is not usable for this source.
func runDVRPUProbe(ctx context.Context, bin, inputPath string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, dvRPUProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(
		probeCtx,
		bin,
		"-hide_banner",
		"-nostats",
		"-v", "error",
		"-i", inputPath,
		"-t", strconv.Itoa(dvRPUProbeSeconds),
		"-map", "0:v:0",
		"-c:v", "copy",
		"-bsf:v", DV7ToHDR10BitstreamFilter,
		"-an", "-sn", "-dn",
		"-f", "null", "-",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return !dvRPUOutputFailed(string(output))
}

// dvRPUOutputFailed spots the filter rejecting packets even when ffmpeg exits 0
// (it treats per-packet filter errors as non-fatal and keeps running).
func dvRPUOutputFailed(output string) bool {
	lowered := strings.ToLower(output)
	return strings.Contains(lowered, "error applying bitstream filters") ||
		strings.Contains(lowered, "failed to read access unit") ||
		strings.Contains(lowered, "failed to read unit")
}
