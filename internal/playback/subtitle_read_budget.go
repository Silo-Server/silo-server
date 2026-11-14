package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrSubtitlePageTooCostly reports that a windowed bitmap extract read more of
// the source container than a page may cost, so it was killed instead of
// running to the extraction timeout. The page is not a source failure: the
// answer is the retryable 503 the caller already understands, and the track's
// progressive artifact is what serves the page once it has parsed that far.
var ErrSubtitlePageTooCostly = errors.New("subtitle page extract read too much of the source")

// SubtitlePageTooCostlyReason is the additive problem code for a page answered
// because its extraction would have read the container prefix to reach it. It
// is distinct from subtitle_source_unavailable so the cost can be told apart
// from a provider that could not be read at all.
const SubtitlePageTooCostlyReason = "subtitle_page_too_costly"

const (
	// subtitlePageReadBudgetBytes caps how much of a remote container one
	// windowed bitmap page extract may read. A page cannot seek a non-seekable
	// relay, so ffmpeg demuxes from byte 0 and discards the prefix: the cost of
	// a page at offset S is (S + W) × bitrate, not W × bitrate. A 120 s page of
	// an 18.25 GB / 35 Mbps source is ~525 MB, which is also the 2-minute
	// extraction timeout, so such pages died at the timeout and returned a
	// partial .sup with no complete display set. The cap is the same order as
	// the largest source a page read is still worth attempting (the implicit
	// window's 256 MB small-source threshold): beyond it the answer is retryable
	// and the progressive artifact is what serves the page.
	subtitlePageReadBudgetBytes = 256 << 20

	// subtitlePageReadBudgetInterval is how often the running extract's own
	// read count is sampled. It is a supervisory check, not a pacing mechanism:
	// the extract is killed at the first sample past the cap.
	subtitlePageReadBudgetInterval = 250 * time.Millisecond
)

// pageReadBudget returns the byte cap for this extract, or 0 when no cap
// applies. Only a bounded bitmap page read from a remote source is capped: a
// whole-track extract must read everything by design, an artifact-backed window
// reads a local file, and a local source's prefix read is a disk read that
// finishes in seconds rather than the relay read the cap exists for.
func (o StreamExtractOpts) pageReadBudget() int64 {
	if !o.pageReadWatch() {
		return 0
	}
	return subtitlePageReadBudgetBytes
}

// pageReadWatch reports whether this extract's own read count is worth watching.
// It is the same set of extracts the cap applies to, minus the cap: the watcher
// also measures the rate the source was read at, which prices the prefix of the
// next page (see subtitle_page_cost.go).
func (o StreamExtractOpts) pageReadWatch() bool {
	if !o.bitmapPage() || !o.windowSeekApplied() {
		return false
	}
	return true
}

// subtitleReadBudget watches how much an extract process has read and cancels
// it once it passes a cap.
//
// ffmpeg has no option to bound how much of its input it reads, so the cap is
// enforced from outside on the process's own I/O counters (/proc/PID/io
// rchar): every read is counted there, including a relay's socket reads, which
// is exactly the cost a page pays. readProcessReadBytes reports false where
// those counters are unavailable (a non-Linux node), and the cap then does not
// apply rather than guessing.
type subtitleReadBudget struct {
	pid      int
	limit    int64
	interval time.Duration
	read     func(pid int) (int64, bool)

	mu     sync.Mutex
	breach int64
	// last is the most recent read count sampled, kept so the extract's read
	// rate can be measured once the process is reaped. It is the total bytes
	// read so far, and the rate is that total over the elapsed time.
	last int64
}

func newSubtitleReadBudget(pid int, limit int64) *subtitleReadBudget {
	return &subtitleReadBudget{
		pid:      pid,
		limit:    limit,
		interval: subtitlePageReadBudgetInterval,
		read:     readProcessReadBytes,
	}
}

// watch samples the process until ctx ends or the cap is passed, then cancels
// the process. Callers run it in its own goroutine. A zero limit watches without
// capping: the samples still measure the rate the source is read at.
func (b *subtitleReadBudget) watch(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			read, ok := b.read(b.pid)
			if !ok {
				continue
			}
			b.mu.Lock()
			b.last = read
			breach := b.limit > 0 && read > b.limit
			if breach {
				b.breach = read
			}
			b.mu.Unlock()
			if breach {
				cancel()
				return
			}
		}
	}
}

// exceeded reports how much the process had read when the cap was passed, or 0
// when the cap was never reached.
func (b *subtitleReadBudget) exceeded() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.breach
}

// observedBytesPerSecond reports the rate the process read its input at, from
// the last sampled count over the extract's elapsed time. ok is false when no
// sample was taken or the counters never reported a read.
func (b *subtitleReadBudget) observedBytesPerSecond(elapsed time.Duration) (float64, bool) {
	if b == nil || elapsed <= 0 {
		return 0, false
	}
	b.mu.Lock()
	last := b.last
	b.mu.Unlock()
	if last <= 0 {
		return 0, false
	}
	return float64(last) / elapsed.Seconds(), true
}

// readProcessReadBytes returns how many bytes the process has read, from its
// procfs I/O counters. ok is false when the counters cannot be read.
func readProcessReadBytes(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, found := strings.CutPrefix(line, "rchar:")
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// startPageReadBudget starts the read-budget watcher for a started extract and
// returns the budget, or nil when this extract's reads are not watched. The
// returned stop func must be called once the process is reaped.
func (o StreamExtractOpts) startPageReadBudget(ctx context.Context, pid int, cancel context.CancelFunc) (*subtitleReadBudget, func()) {
	if !o.pageReadWatch() || pid <= 0 {
		return nil, func() {}
	}
	budget := newSubtitleReadBudget(pid, o.pageReadBudget())
	watchCtx, stop := context.WithCancel(ctx)
	go budget.watch(watchCtx, cancel)
	return budget, stop
}

// pageReadBudgetError reports the budget breach as its sentinel error.
func (b *subtitleReadBudget) pageReadBudgetError() error {
	read := b.exceeded()
	if read == 0 {
		return nil
	}
	return fmt.Errorf("%w: read %d bytes of the source for one subtitle window (cap %d)",
		ErrSubtitlePageTooCostly, read, b.limit)
}
