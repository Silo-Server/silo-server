/**
 * Bounds the player's own recovery of a failed HLS load.
 *
 * hls.js retries a fragment a few times inside one load, then reports the
 * failure as fatal. The player answered that by starting the load again — and
 * on a stream that has already played, nothing counted those recoveries at
 * all. A corrupt source then asks the segment layer for the same fragment on
 * every recovery: one deployment fetched `seg_00000.ts` 147 times in a
 * two-minute window while the server logged a frame-RPS error for every
 * request. The stream could never succeed, so the retries only multiplied
 * server and relay load.
 *
 * The budget separates the two cases the recovery has to tell apart:
 *
 * - A transport hiccup. A failure on a segment the transport had not reached
 *   before is retried, and any load that completes clears the streak, so a
 *   long session can survive several unrelated blips.
 * - Repeated failure on the same segment. A fragment that keeps failing is not
 *   a hiccup, so its own count is capped well below the streak cap.
 *
 * Reaching either cap is a failed route: the caller surfaces the ordinary
 * failure UI (a replan) instead of requesting the segment again.
 */

/** Failures of one segment index before the load is treated as a failure. */
export const MAX_SAME_SEGMENT_FAILURES = 2;

/**
 * Failures without a completed load before the load is treated as a failure.
 * This catches a source where every segment fails at a different index, which
 * the per-segment cap alone would retry forever.
 */
export const MAX_STREAK_FAILURES = 3;

const BASE_RETRY_DELAY_MS = 1_000;
const MAX_RETRY_DELAY_MS = 8_000;

/**
 * How long to wait before the load's next attempt. The first retry is quick
 * enough to hide a blip; later ones back off so a failing source is not asked
 * for the same bytes at the rate hls.js would.
 */
export function segmentRetryDelayMs(attempt: number): number {
  const exponent = Number.isFinite(attempt) ? Math.max(0, Math.floor(attempt)) : 0;
  return Math.min(BASE_RETRY_DELAY_MS * 2 ** exponent, MAX_RETRY_DELAY_MS);
}

/**
 * Counts a transport's failed loads between completed ones.
 *
 * `key` identifies the fragment (its media sequence number), or is null for a
 * failure that belongs to the playlist or manifest rather than a segment.
 */
export class SegmentRetryBudget {
  private streak = 0;
  private readonly failures = new Map<string | null, number>();

  /**
   * Records a failed load of `key` and returns the retry attempt number to
   * back off with, or null when the transport has spent its budget and the
   * route has failed.
   */
  recordFailure(key: string | null): number | null {
    const sameSegment = (this.failures.get(key) ?? 0) + 1;
    this.failures.set(key, sameSegment);
    if (sameSegment >= MAX_SAME_SEGMENT_FAILURES) return null;
    if (this.streak >= MAX_STREAK_FAILURES) return null;
    return this.streak++;
  }

  /** A load that completed proves the transport works; the streak starts over. */
  reset(): void {
    this.streak = 0;
    this.failures.clear();
  }
}
