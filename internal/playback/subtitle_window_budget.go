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
// already backs off from (503 with Retry-After) and the recovery warm the
// handler starts for it.
var errSubtitleWindowBudget = errors.New("subtitle window extract exceeded its budget")

const (
	// subtitleTextWindowClientStall is the client's own subtitle fetch stall
	// deadline (web: SUBTITLE_FETCH_STALL_TIMEOUT_MS). Past it the fetcher
	// aborts the request, so a server bound beyond it can never deliver a body:
	// the read is thrown away and the client learns nothing.
	subtitleTextWindowClientStall = 30 * time.Second

	// subtitleTextWindowAnswerReserve is what the bound leaves inside the
	// client's stall for ffmpeg's muxer header, the first-cue gate and the
	// response write, plus the layout probe a first request on a track may
	// have paid earlier in the same request.
	subtitleTextWindowAnswerReserve = 4 * time.Second

	// subtitleTextWindowElapsedBudget bounds how long one request-path text
	// window extract may run before it is killed and answered retryably.
	//
	// It is derived from the client contract, not from a fixed guess. The read
	// this bound covers is the window's own span, because ffmpeg seeks the
	// relay input: a 120 s page of a 75 GB / 81 Mbps release is ~1.2 GB of
	// container, which the relay that produced the 4K measurements served in
	// ~20 s (60 MB/s). The previous 20 s bound was below that, so every 4K
	// window was killed after its bytes were already produced and answered as
	// a source failure. 26 s (the 30 s client stall minus the answer reserve)
	// covers the measured cost with room for startup; a source slower than
	// that cannot be answered inside the client's stall at any bound, so its
	// window is answered 503 with Retry-After and served from the covering
	// warm this refusal starts.
	//
	// This cannot be a byte cap on the extract's input. ffmpeg reads an HTTP
	// input with recv(2), not read(2), and /proc/PID/io counts only
	// read(2)/pread(2) in rchar, so the relay read is invisible there. Elapsed
	// time is the only bound that sees the relay read at all, so it is the
	// honest one here.
	subtitleTextWindowElapsedBudget = subtitleTextWindowClientStall - subtitleTextWindowAnswerReserve
)

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
