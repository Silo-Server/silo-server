package playback

import (
	"math"
	"strings"
)

// Paging a remote bitmap track: what a page may cost before it is worth
// extracting at all.
//
// A bounded bitmap page is answered by reading the container from byte zero and
// discarding everything before the window, because a relay input has no seek the
// demuxer can trust. A page therefore pays for its whole prefix, not just its own
// span, and the prefix is the part that hurts: a page at 3600 s of an 18 GB
// release read gigabytes of container in 88 s in production while its read count
// stayed under the runtime cap, since the relay answered the seek with range
// reads. A byte counter alone cannot tell that page from a cheap one.
//
// The gate below is the pre-extract estimate. It refuses a page when the prefix
// it would have to read is beyond the budget, in bytes or in time at the rate
// this identity's extracts have been observed to read. A refusal is the
// retryable 503 the client already backs off from, advertises the progressive
// artifact's coverage in X-Subtitle-Coverage, and is what starts that artifact's
// fill (the API handler owns the fill because its relay registration outlives
// the request).
//
// An opening page is never refused: its prefix is zero, so it extracts exactly as
// before. The page itself is what the viewer asked for; only the discarded prefix
// is bounded.
const (
	// subtitlePagePrefixBudgetBytes caps the container prefix a bounded bitmap
	// page may read. It is the same order as the largest source a page read is
	// still worth attempting (the implicit window's 256 MB small-source
	// threshold): beyond it the page is served from the track's progressive
	// artifact instead.
	subtitlePagePrefixBudgetBytes int64 = 256 << 20

	// subtitlePagePrefixBudgetSeconds caps the same prefix in wall-clock terms,
	// at the read rate this identity has been observed to sustain. A prefix
	// under the byte cap can still be too slow to answer inside a client fetch
	// (production read an 88 s page whose byte count fit), so bytes alone do
	// not decide. The bound sits well inside the two-minute window extract
	// timeout: a page that would spend most of that timeout discarding a prefix
	// is one the artifact should answer.
	subtitlePagePrefixBudgetSeconds = 45.0

	// subtitlePageReadRatesMaxEntries caps the per-identity observed-read-rate
	// map. One entry per source identity is small; the cap only keeps a
	// long-lived node from retaining a rate for every track it ever paged.
	subtitlePageReadRatesMaxEntries = 1024
)

// bitmapPage reports whether opts is a bounded bitmap window over a remote
// container: the only extract whose cost is a container prefix. A whole-track
// extract must read everything by design, an artifact-backed window reads a
// local file, and a local source's prefix read is a disk read.
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

// pagePrefixEstimate prices the container prefix this page would read: bytes when
// the container's rate is known, and wall-clock seconds when this identity's read
// rate has been observed. priced is false when nothing known can price the prefix,
// in which case the page keeps its source extract (the runtime read cap still
// applies to it) rather than being refused for an assumption.
func (o StreamExtractOpts) pagePrefixEstimate(observedBytesPerSecond float64) (prefixBytes int64, prefixSeconds float64, priced bool) {
	if !o.bitmapPage() || !o.windowSeekApplied() {
		return 0, 0, false
	}
	windowed, start, _, openEnded := o.WindowCoverage()
	if !windowed || openEnded {
		return 0, 0, false
	}
	if start <= 0 {
		// An opening page has no discarded prefix, so it is always affordable
		// whatever else is known about the source.
		return 0, 0, true
	}
	byteRate, known := o.sourceBytesPerSecond()
	if !known {
		byteRate = observedBytesPerSecond
	}
	if !(byteRate > 0) {
		return 0, 0, false
	}
	prefixBytes = int64(math.Ceil(start * byteRate))
	if prefixBytes < 0 {
		return 0, 0, false
	}
	// A prefix cannot hold more than the container itself. A seek past the end
	// of the source, or a size and bitrate that disagree, would otherwise price
	// the page above the whole source and refuse a page the source can serve.
	if size := o.SourceSizeBytes; size > 0 && prefixBytes > size {
		prefixBytes = size
	}
	if observedBytesPerSecond > 0 {
		prefixSeconds = float64(prefixBytes) / observedBytesPerSecond
	}
	return prefixBytes, prefixSeconds, true
}

// pageTooCostlyRetry is the retryable answer for a page the source cannot serve
// at an affordable read cost: the client backs off, learns how far the track's
// progressive artifact has parsed, and the handler starts that fill. It is shared
// by the pre-extract refusal, the runtime read cap, and a page that ran out its
// extract deadline, so all three answer the same way.
func (c *SubtitleCache) pageTooCostlyRetry(opts StreamExtractOpts) *SubtitleExtractionRetryError {
	_, covered := c.progressiveSUPArtifact(opts)
	return &SubtitleExtractionRetryError{
		RetryAfter:   subtitlePageCostlyRetryAfter,
		Reason:       SubtitlePageTooCostlyReason,
		CoveredUntil: covered,
	}
}

// pagePrefixBeyondBudget reports whether the container prefix this page would
// read is priced beyond the page budget, in bytes or in time at the rate this
// identity's extracts have been observed to read. An unpriceable page is not
// beyond budget: it keeps its source extract (the runtime read cap still applies)
// rather than being refused for an assumption.
func (c *SubtitleCache) pagePrefixBeyondBudget(opts StreamExtractOpts) bool {
	observedRate, _ := c.pageReadRate(opts.CacheIdentity)
	prefixBytes, prefixSeconds, priced := opts.pagePrefixEstimate(observedRate)
	if !priced {
		return false
	}
	return prefixBytes > subtitlePagePrefixBudgetBytes || prefixSeconds > subtitlePagePrefixBudgetSeconds
}

// pagePrefixRefusal prices this page's source read before it starts and returns
// the retryable shape when the page must wait for the track's progressive
// artifact instead. It reads only the cache's own bookkeeping, so no provider
// read happens before the answer.
func (c *SubtitleCache) pagePrefixRefusal(opts StreamExtractOpts) *SubtitleExtractionRetryError {
	if !c.pagePrefixBeyondBudget(opts) {
		return nil
	}
	recordSubtitlePageCostRefused()
	return c.pageTooCostlyRetry(opts)
}

// notePageReadRate records how fast one extract of identity's source read, so a
// later page's prefix can be priced in time and not only in bytes. The newest
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
