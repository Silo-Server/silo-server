package playback

import (
	"context"
	"errors"
	"strings"
	"time"
)

// errSubtitleWindowBudget is the cancellation cause a request-path text window
// extract is killed with when it passes its elapsed budget. It never reaches a
// client: SubtitleCache turns the breach into the retryable shape the client
// already backs off from (503 with Retry-After).
var errSubtitleWindowBudget = errors.New("subtitle window extract exceeded its budget")

// subtitleTextWindowElapsedBudget bounds how long one request-path text window
// extract may run before it is killed and answered retryably.
//
// A 120 s page on a relay that delivers 3-7x realtime needs 17-40 s of source
// before ffmpeg can emit the first cue, and the deployed build measured
// 25.9-58.1 s of silence on exactly that path (24 subtitle requests in the
// window: p50 1011 ms, p90 49327 ms, max 58147 ms). The client's own subtitle
// fetch stall deadline is 30 s (SUBTITLE_FETCH_STALL_TIMEOUT_MS), so it aborted
// before the server answered: the two observed requests had status 0, and the
// two-minute window extract timeout never fired. Nothing was learned and the
// read was repeated on the next attempt.
//
// This cannot be a byte cap on the extract's input. ffmpeg reads an HTTP input
// with recv(2), not read(2), and /proc/PID/io counts only read(2)/pread(2) in
// rchar: measured against a relay that served 24 MB, ffmpeg's rchar stayed at
// the ~181 KB of reads it does at startup. Elapsed time is the only bound that
// sees the relay read at all, so it is the honest one here.
//
// 20 s leaves the client 10 s of its stall budget for ffmpeg startup, the
// response write, and a layout probe that ran earlier in the same request. An
// opening page on a fast relay still finishes inside it; a page that cannot is
// answered 503 with Retry-After and leaves a warm running, so the retry reads a
// committed slice instead of repeating the read.
const subtitleTextWindowElapsedBudget = 20 * time.Second

// subtitleWindowBudgeted reports whether this extract is the bounded text
// window a client is waiting on, and so whether the request-path elapsed budget
// applies to it.
//
// Out of scope, and why:
//   - a bitmap (PGS) page has its own read budget and progressive artifact;
//   - a whole-track extract must read everything by design;
//   - a committed text artifact, or packets read through the source's index, is
//     a local read rather than the relay demux this bound exists for;
//   - a detached prefetch or upgrade probe serves no client, so it is allowed
//     the full window timeout: on a slow-but-working relay its completed slice
//     is what makes the client's next attempt cheap.
func (o StreamExtractOpts) subtitleWindowBudgeted() bool {
	if IsPGS(o.SourceCodec) || !o.windowSeekApplied() {
		return false
	}
	if o.Packets != nil || o.InputIsExtractedSup || o.InputIsExtractedText != "" {
		return false
	}
	if strings.TrimSpace(o.CacheIdentity) == "" {
		return false
	}
	return !o.DetachedRelayDemux
}

// textWindowExtractBudget returns how long this cache may spend on one
// request-path text window extract before answering retryable, or 0 when the
// extract is not bounded by the request-path budget. NewSubtitleCache installs
// the production bound; tests override it on their own cache.
func (c *SubtitleCache) textWindowExtractBudget(opts StreamExtractOpts) time.Duration {
	if !opts.subtitleWindowBudgeted() {
		return 0
	}
	if c != nil && c.windowElapsedBudget > 0 && c.windowElapsedBudget < subtitleTextWindowElapsedBudget {
		return c.windowElapsedBudget
	}
	return subtitleTextWindowElapsedBudget
}

// windowExtractBounded reports whether extractCtx was canceled by this
// window's own budget rather than by the request, the window timeout, or the
// extract failing.
func windowExtractBounded(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errSubtitleWindowBudget)
}

// SetWindowExtractBudgetForTest overrides the request-path text window elapsed
// budget so a test can exercise the retryable answer without waiting out the
// production bound. A zero or negative budget restores the production one.
func (c *SubtitleCache) SetWindowExtractBudgetForTest(budget time.Duration) {
	if c == nil {
		return
	}
	c.windowElapsedBudget = budget
}
