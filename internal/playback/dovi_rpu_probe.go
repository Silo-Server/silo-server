package playback

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Some Dolby Vision sources carry an RPU that ffmpeg's dovi_rpu bitstream
// filter cannot parse. The filter does not fail cleanly: it rejects every
// packet, and ffmpeg keeps going, emitting a pair of errors per frame — one
// observed session produced 376,316 stderr lines before the process was
// killed. Playback never starts, the manifest build fails, and the client is
// handed a 503 after ~10 seconds, which it shows as an endless spinner:
//
//	[dovi_rpu] Failed to read unit 1 (type 39).
//	[vost#0:0/copy] Error applying bitstream filters to a packet:
//	    Invalid data found ... Invalid SEI message: payload_size too large
//
// Whether a given file survives the strip is a property of that file, not of
// ffmpeg, so it cannot be answered by supportsDoviRPUFilter. It can be answered
// in about a second by asking ffmpeg to strip a couple of seconds to nowhere.
//
// The probe is a planning input, not a transport patch: a source that fails it
// must not be planned onto a strip route at all, because the plan's HDR10
// promise and the durable session's RemuxDVMode are both derived from that
// decision and are re-read on every later restart.
const (
	// Enough packets to reach the RPU. This catches the observed failure, in
	// which the filter rejects the very first access unit and every one after
	// it. It does not certify the whole title: a source that parses cleanly at
	// the head and breaks an hour in still fails mid-stream, which is the
	// separate "bail out on repeated per-packet filter errors" problem.
	dvRPUProbeSeconds = 2
	// A copy of two seconds of video decodes nothing, so this is generous even
	// for a cold spinning disk or a network mount. A source that cannot hand
	// over two seconds within it cannot feed a live transcode either, and the
	// timeout is inconclusive rather than fatal: the strip is kept and nothing
	// is cached. Sized to stay well inside the ~10s a client waits on the
	// manifest, so a slow probe cannot itself become the spinner.
	dvRPUProbeTimeout = 6 * time.Second
	// The rejection markers appear in the first few lines; a broken source
	// killed at the timeout can otherwise pile up hundreds of thousands more.
	maxDVRPUProbeOutput = 64 << 10
	// Same reasoning as the letterbox cache: bounded so a long-lived server
	// with a large library cannot accumulate an entry per file forever.
	maxDVRPUProbeEntries = 4096
)

// dvRPUVerdict separates "this source rejects the strip" from "the probe did
// not find out". Only the former may be cached: a probe cancelled with the
// client's request, or one that timed out on a cold mount, says nothing about
// the file and must not disable the strip for every later viewer.
type dvRPUVerdict int

const (
	dvRPUUnknown dvRPUVerdict = iota
	dvRPUStrippable
	dvRPUBroken
)

// DVRPUProbe records, per source file, whether the Dolby Vision RPU strip
// works. The zero value is not usable; call NewDVRPUProbe.
type DVRPUProbe struct {
	mu       sync.Mutex
	results  map[string]bool
	order    []string
	inflight map[string]*dvRPUCall
}

// dvRPUCall is one in-flight probe that concurrent callers for the same source
// share instead of each spawning their own ffmpeg.
type dvRPUCall struct {
	done       chan struct{}
	strippable bool
}

func NewDVRPUProbe() *DVRPUProbe {
	return &DVRPUProbe{
		results:  make(map[string]bool),
		inflight: make(map[string]*dvRPUCall),
	}
}

// sharedDVRPUProbe backs DVRPUStrippable. One cache per process, like
// doviRPUCache: the planner, the progressive remux and the restart endpoints
// all ask the same question about the same files and must agree, and none of
// them should pay for a probe another already ran.
var sharedDVRPUProbe = NewDVRPUProbe()

// DVRPUStrippable reports whether the RPU strip should be attempted for this
// source. ffmpegPath is the configured playback binary (empty selects the
// process-global discovery), matching every other capability probe here.
//
// Unknown sources are probed inline — this runs on the playback-start path,
// where a second of certainty is worth far more than handing the client a
// stream that cannot start. Only sources the planner would actually strip ever
// reach here, so the cost lands on a small minority of titles, once each.
func DVRPUStrippable(ctx context.Context, ffmpegPath, inputPath string) bool {
	return sharedDVRPUProbe.CanStrip(ctx, ResolveFFmpegPath(ffmpegPath), inputPath)
}

// CanStrip answers for one source, probing it if the verdict is not cached.
// It fails open: anything that stops the probe from reaching a conclusion
// keeps the previous strip-always behaviour, because most Profile 7 sources
// genuinely need the strip and "we did not find out" must not silently
// disable it.
func (p *DVRPUProbe) CanStrip(ctx context.Context, bin, inputPath string) bool {
	if p == nil || strings.TrimSpace(inputPath) == "" {
		return true
	}
	key, ok := dvRPUProbeKey(bin, inputPath)
	if !ok {
		// The file cannot even be stat'd; leave the verdict to whatever
		// actually tries to open it.
		return true
	}

	p.mu.Lock()
	if strippable, cached := p.results[key]; cached {
		p.mu.Unlock()
		return strippable
	}
	if call, running := p.inflight[key]; running {
		p.mu.Unlock()
		select {
		case <-call.done:
			return call.strippable
		case <-ctx.Done():
			return true
		}
	}
	call := &dvRPUCall{done: make(chan struct{}), strippable: true}
	p.inflight[key] = call
	p.mu.Unlock()

	// Detached from the caller: the leader is probing on behalf of every
	// follower queued behind it, so its own client giving up must not turn a
	// verdict the others are waiting on into an inconclusive fail-open — that
	// would hand a live session the strip this source cannot survive. The
	// probe stays bounded by dvRPUProbeTimeout, and a verdict reached after
	// the leader has left is still worth caching for the next start.
	started := time.Now()
	verdict := runDVRPUProbe(context.WithoutCancel(ctx), bin, inputPath)
	call.strippable = verdict != dvRPUBroken

	p.mu.Lock()
	delete(p.inflight, key)
	if verdict != dvRPUUnknown {
		if _, exists := p.results[key]; !exists {
			p.order = append(p.order, key)
		}
		p.results[key] = call.strippable
		p.trimLocked()
	}
	p.mu.Unlock()
	close(call.done)

	slog.InfoContext(ctx, "dolby vision rpu strip probed",
		"component", "playback",
		"input", inputPath,
		"can_strip", call.strippable,
		"conclusive", verdict != dvRPUUnknown,
		"took_ms", time.Since(started).Milliseconds(),
	)
	return call.strippable
}

// dvRPUProbeKey identifies a probe result. Keyed on size and modification time
// as well as path so a file replaced in place is re-probed rather than
// inheriting the old verdict, and on the binary because a different ffmpeg
// build can parse a different set of RPUs.
func dvRPUProbeKey(bin, inputPath string) (string, bool) {
	info, err := os.Stat(inputPath)
	if err != nil {
		return "", false
	}
	return strings.Join([]string{
		bin,
		inputPath,
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, "|"), true
}

// trimLocked bounds the cache, oldest first. A wrong eviction costs one extra
// probe of a file nobody has played in a long time.
func (p *DVRPUProbe) trimLocked() {
	for len(p.order) > maxDVRPUProbeEntries {
		delete(p.results, p.order[0])
		p.order = p.order[1:]
	}
}

// runDVRPUProbe strips a short head of the file to the null muxer.
//
// stderr is the signal, not the exit code: ffmpeg treats a per-packet
// bitstream-filter error as non-fatal and exits 0, which is precisely how a
// stream that could never start reached a live session in the first place. A
// rejection on stderr is therefore checked first and is conclusive whether or
// not the process also died — a source killed at the timeout mid-flood is
// broken, not merely slow.
func runDVRPUProbe(ctx context.Context, bin, inputPath string) dvRPUVerdict {
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
	output := &cappedBuffer{limit: maxDVRPUProbeOutput}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()

	if dvRPUOutputFailed(output.String()) {
		return dvRPUBroken
	}
	if probeCtx.Err() != nil || ctx.Err() != nil {
		// The run ended on the deadline rather than on ffmpeg's own verdict,
		// with the filter having said nothing. Nothing was learned about the
		// file. CanStrip detaches from the request, so in practice this is the
		// timeout; the ctx check keeps the rule right for any other caller.
		return dvRPUUnknown
	}
	if err != nil {
		// ffmpeg could not run, or failed for a reason unrelated to the RPU
		// (an unreadable mount, a missing binary). Not a verdict on the strip.
		return dvRPUUnknown
	}
	return dvRPUStrippable
}

// dvRPUOutputFailed spots the filter rejecting packets even when ffmpeg exits 0
// (it treats per-packet filter errors as non-fatal and keeps running).
func dvRPUOutputFailed(output string) bool {
	lowered := strings.ToLower(output)
	return strings.Contains(lowered, "error applying bitstream filters") ||
		strings.Contains(lowered, "failed to read access unit") ||
		strings.Contains(lowered, "failed to read unit")
}

// cappedBuffer keeps the head of a stream and discards the rest, so a filter
// failing once per frame cannot turn a diagnostic into a memory problem.
type cappedBuffer struct {
	limit int
	buf   []byte
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - len(c.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.buf = append(c.buf, p[:room]...)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return string(c.buf) }
