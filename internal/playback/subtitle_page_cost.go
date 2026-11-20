package playback

import (
	"math"
	"strings"
)

// Paging a remote bitmap track: what a page may cost before it is worth
// extracting at all.
//
// A bounded bitmap page is answered by an input seek to the page's start
// (`-ss <start>` before `-i`, `-copyts` after it), so against a seekable source
// it reads its own window plus a fixed opening cost, not the container prefix
// before it. That was measured, because the two behaviors differ by an order of
// magnitude and the old gate assumed the expensive one:
//
//	container            source              bytes read for a 120 s page at 3000 s of 3600 s
//	MKV, cues at end     Range honored       13.0 MB  (5.9 probe+header, 3.4 first cluster, 3.6 kB cues, 5.8 at the seek offset)
//	MKV, cues stripped   Range honored      114.0 MB  (linear scan from the first cluster)
//	MP4, moov in front   Range honored        8.3 MB  (4.0 probe, 4.3 at the seek offset)
//	MPEG-TS              Range honored       33.7 MB  (14 MB probe, ~5 MB index binary search, 8 MB at the seek offset)
//	MKV, cues at end     Range ignored        93.9 MB  (one `bytes=0-` 200 read to the window end)
//	MPEG-TS              Range ignored       240.9 MB  (three from-zero reads: non-seekable input re-opens)
//
// The whole 101 MB MKV reads 102 MB, so the cued-MKV page read is 1/8 of the
// whole track and 1/7 of what the same page read without a usable index.
//
// The relay's own behavior is the deciding half of that: it forwards Range and
// strips Accept-Ranges only when its upstream answered a range request with
// 200 (internal/remotestream/relay.go), so "the relay answered the seek with
// range reads" says nothing about seeking — a linear prefix scan is served as
// one `bytes=0-` 206 too. The index is the other half: Matroska cannot seek
// without its cues, and no provider can seek without Range.
//
// Neither is knowable before the extract without reading the container, so the
// estimate below prices the window: refusing a page for a prefix it probably
// will not read is what kept every deep bitmap page off production for good —
// the request carried a bounded window and was answered `subtitle_page_too_costly`
// in 6-12 ms, 27 times, without a byte read. A page that does turn out to scan
// is bounded by the runtime read cap (subtitle_read_budget.go), which is sized
// from the same window estimate, and by the window extract deadline.
//
// The refusal that remains is a page whose *own* window cannot be read inside
// the page extract budget at the rate this identity reads at. That is a real
// cost, not an assumption: it is answered retryably, advertises the progressive
// artifact's coverage in X-Subtitle-Coverage, and starts that artifact's fill
// (the API handler owns the fill because its relay registration outlives the
// request).
//
// An opening page is never refused: its window is zero-prefixed, so it extracts
// exactly as before.
const (
	// subtitlePageWindowBudgetSeconds is the wall clock a page's own window read
	// may take: one page extract deadline. A window that cannot be demuxed in
	// that time cannot answer the request that asked for it, whatever its
	// offset. It is the same bound the request-path extract enforces
	// (subtitleWindowExtractTimeout), so the pre-extract price and the extract
	// itself agree.
	subtitlePageWindowBudgetSeconds = 120.0

	// subtitlePageWindowBudgetBytes caps the same window in bytes when no read
	// rate has been observed for this identity yet. It is deliberately far
	// above a 120 s page of any plausible 4K remux (~300-550 MB): the point is
	// to stop a multi-page or open-ended window, not a normal page.
	subtitlePageWindowBudgetBytes int64 = 1536 << 20

	// subtitlePageReadRatesMaxEntries caps the per-identity observed-read-rate
	// map. One entry per source identity is small; the cap only keeps a
	// long-lived node from retaining a rate for every track it ever paged.
	subtitlePageReadRatesMaxEntries = 1024
)

// bitmapPage reports whether opts is a bounded bitmap window over a remote
// container: the only extract whose cost was ever a container prefix. A
// whole-track extract must read everything by design, an artifact-backed window
// reads a local file, and a local source's window read is a disk read.
func (o StreamExtractOpts) bitmapPage() bool {
	if !IsPGS(o.SourceCodec) || o.InputIsExtractedSup || o.InputIsExtractedText != "" || o.Packets != nil {
		return false
	}
	return strings.TrimSpace(o.CacheIdentity) != ""
}

// sourceBytesPerSecond estimates the container's average byte rate from what the
// catalog knows about it: its size against its runtime when both are known,
// otherwise the bitrate a probe recorded.
func (o StreamExtractOpts) sourceBytesPerSecond() (float64, bool) {
	if o.SourceSizeBytes > 0 && o.SourceDurationSeconds > 0 {
		if rate := float64(o.SourceSizeBytes) / o.SourceDurationSeconds; rate > 0 {
			return rate, true
		}
	}
	if o.SourceBitrateKbps > 0 {
		return float64(o.SourceBitrateKbps) * 1000 / 8, true
	}
	return 0, false
}

// pageWindowEstimate prices the window this page would read: bytes of container
// covering the window's span, and wall-clock seconds when this identity's read
// rate has been observed. priced is false when nothing known can price it, in
// which case the page keeps its source extract (the runtime read cap still
// applies to it) rather than being refused for an assumption.
func (o StreamExtractOpts) pageWindowEstimate(observedBytesPerSecond float64) (windowBytes int64, windowSeconds float64, priced bool) {
	if !o.bitmapPage() || !o.windowSeekApplied() {
		return 0, 0, false
	}
	windowed, start, end, openEnded := o.WindowCoverage()
	if !windowed || openEnded {
		return 0, 0, false
	}
	span := end - start
	if span <= 0 {
		return 0, 0, false
	}
	byteRate, known := o.sourceBytesPerSecond()
	if !known || !(byteRate > 0) {
		// The window's size is a property of the container, not of the link: a
		// fast link says nothing about how many bytes 120 s of this source holds.
		// Without the container's byte rate the page is unpriced and keeps its
		// source extract under the runtime read cap.
		return 0, 0, false
	}
	windowBytes = int64(math.Ceil(span * byteRate))
	if windowBytes < 0 {
		return 0, 0, false
	}
	// A window cannot hold more than the container itself. A size and bitrate
	// that disagree, or a window reaching past the end of the source, would
	// otherwise price the page above the whole source and refuse a page the
	// source can serve.
	if size := o.SourceSizeBytes; size > 0 && windowBytes > size {
		windowBytes = size
	}
	if observedBytesPerSecond > 0 {
		windowSeconds = float64(windowBytes) / observedBytesPerSecond
	}
	return windowBytes, windowSeconds, true
}

// pageTooCostlyRetry is the answer for a page the source cannot serve at an
// affordable read cost: while the track's progressive artifact may still reach
// the page the client backs off and the first refusal starts that fill; once the
// wait budget is spent the answer is final (see subtitle_page_recovery.go). It is
// shared by the pre-extract refusal, the runtime read cap, and a page that ran out
// its extract deadline, so all three answer the same way.
func (c *SubtitleCache) pageTooCostlyRetry(opts StreamExtractOpts) error {
	start, _, _ := progressiveSUPWindow(opts)
	_, covered := c.progressiveSUPArtifact(opts)
	return c.pageRecoveryRefusal(opts, start, covered, subtitlePageCostlyRetryAfter, SubtitlePageTooCostlyReason)
}

// pageWindowBeyondBudget reports whether the window this page would read is
// priced beyond the page budget, in bytes or in time at the rate this identity's
// extracts have been observed to read. An unpriceable page is not beyond budget:
// it keeps its source extract (the runtime read cap still applies) rather than
// being refused for an assumption.
func (c *SubtitleCache) pageWindowBeyondBudget(opts StreamExtractOpts) bool {
	observedRate, _ := c.pageReadRate(opts.CacheIdentity)
	windowBytes, windowSeconds, priced := opts.pageWindowEstimate(observedRate)
	if !priced {
		return false
	}
	byteBudget := subtitlePageWindowBudgetBytes
	if observedRate > 0 {
		if timed := int64(math.Ceil(observedRate * subtitlePageWindowBudgetSeconds)); timed < byteBudget {
			byteBudget = timed
		}
	}
	return windowBytes > byteBudget || windowSeconds > subtitlePageWindowBudgetSeconds
}

// pageWindowRefusal prices this page's window before its extract starts and
// returns the retryable shape when the window cannot be read inside one page
// extract. It reads only the cache's own bookkeeping, so no provider read
// happens before the answer.
func (c *SubtitleCache) pageWindowRefusal(opts StreamExtractOpts) error {
	if !c.pageWindowBeyondBudget(opts) {
		return nil
	}
	recordSubtitlePageCostRefused()
	return c.pageTooCostlyRetry(opts)
}

// notePageReadRate records how fast one extract of identity's source read, so a
// later page's window can be priced in time and not only in bytes. The newest
// sample replaces the previous one: the page that just read this source is the
// best available evidence of how fast the next one would read it.
func (c *SubtitleCache) notePageReadRate(identity string, bytesPerSecond float64) {
	if c == nil || strings.TrimSpace(identity) == "" || !(bytesPerSecond > 0) {
		return
	}
	c.pageCostMu.Lock()
	defer c.pageCostMu.Unlock()
	if c.pageReadRates == nil {
		c.pageReadRates = make(map[string]float64)
	}
	if len(c.pageReadRates) >= subtitlePageReadRatesMaxEntries {
		for key := range c.pageReadRates {
			delete(c.pageReadRates, key)
			break
		}
	}
	c.pageReadRates[identity] = bytesPerSecond
}

// pageReadRate returns the last rate observed for identity, if any.
func (c *SubtitleCache) pageReadRate(identity string) (float64, bool) {
	if c == nil || strings.TrimSpace(identity) == "" {
		return 0, false
	}
	c.pageCostMu.Lock()
	defer c.pageCostMu.Unlock()
	rate, ok := c.pageReadRates[identity]
	if !ok || !(rate > 0) {
		return 0, false
	}
	return rate, true
}
