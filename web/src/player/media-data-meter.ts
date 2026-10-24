import { MAX_PROBE_KBPS, MIN_PROBE_KBPS } from "./probe-meter";

/**
 * The least receiving time a request needs to count toward the plan's
 * throughput. A short request on a warm connection, such as one HLS segment,
 * arrives at the connection's burst rate, which can be twice what the link
 * sustains.
 */
const MIN_THROUGHPUT_SAMPLE_MS = 1_000;

/** How a request is doing: its bytes so far and when its first one arrived. */
export interface MediaRequestProgress {
  bytes: number;
  first: number;
  /** The request ended, completed or not; it is read no more. */
  done: boolean;
}

/**
 * What a player sees of one plan's media requests: video and audio segments,
 * or the reads of a progressive file, never subtitles. It answers when media
 * data last arrived, for the outage rule, and the rate the plan's long
 * requests received data at, for plan_summary `throughput_kbps` and a
 * lowering's `observed_throughput_kbps` (docs/design/2026-10-17-buffering-quality.md,
 * Playback samples). Times are `performance.now()` milliseconds.
 */
export class MediaDataMeter {
  private dataAt: number;
  private requests = new Map<unknown, { first: number; last: number; bytes: number }>();
  private watched = new Map<unknown, () => MediaRequestProgress>();

  /** Until its first byte, silence counts from `startedAt`, when the plan's loading began. */
  constructor(startedAt: number) {
    this.dataAt = startedAt;
  }

  /**
   * `request` has received `bytes` in all, the first of them at `first` and
   * the latest at `last`. A request reports again as more arrives.
   */
  received(request: unknown, bytes: number, first: number, last: number) {
    if (bytes <= 0) return;
    this.requests.set(request, { first, last, bytes });
    this.dataAt = Math.max(this.dataAt, last);
  }

  /**
   * Follows a request whose progress is read rather than reported, as hls.js
   * fills in a fragment's stats while it loads. Data a read finds arrived by
   * the time of that read.
   */
  watch(request: unknown, progress: () => MediaRequestProgress) {
    this.watched.set(request, progress);
  }

  /** When media data last arrived, after reading the requests it follows. */
  lastDataAt(now: number): number {
    for (const [request, progress] of this.watched) {
      const { bytes, first, done } = progress();
      if (bytes > (this.requests.get(request)?.bytes ?? 0))
        this.received(request, bytes, first, now);
      if (done) this.watched.delete(request);
    }
    return this.dataAt;
  }

  /**
   * The rate the requests that spent at least a second receiving got their
   * data at, with time in which several were receiving counted once; capped
   * at 1,000,000 kbps. Undefined when nothing counts or the rate is under the
   * 100 kbps a measurement needs.
   */
  throughputKbps(now: number): number | undefined {
    this.lastDataAt(now);
    let bytes = 0;
    const spans: [number, number][] = [];
    for (const { first, last, bytes: received } of this.requests.values()) {
      if (last - first < MIN_THROUGHPUT_SAMPLE_MS) continue;
      bytes += received;
      spans.push([first, last]);
    }
    // A long plan keeps thousands of segment requests, so sort them once and
    // count overlapping time in one pass.
    spans.sort((a, b) => a[0] - b[0]);
    let ms = 0;
    let end = -Infinity;
    for (const [first, last] of spans) {
      if (last <= end) continue;
      ms += last - Math.max(first, end);
      end = last;
    }
    if (ms < MIN_THROUGHPUT_SAMPLE_MS) return undefined;
    const kbps = Math.min(Math.floor((bytes * 8) / ms), MAX_PROBE_KBPS);
    return kbps >= MIN_PROBE_KBPS ? kbps : undefined;
  }
}
